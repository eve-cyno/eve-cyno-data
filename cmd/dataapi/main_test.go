package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
	"eve-cyno.dev/go/data/tools"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestListenAddr(t *testing.T) {
	require.Equal(t, ":8092", listenAddr(env(nil)))
	require.Equal(t, "127.0.0.1:9000", listenAddr(env(map[string]string{"DATAAPI_ADDR": "127.0.0.1:9000"})))
}

func TestExposeToolAPI_FailsClosed(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage", "tru"} {
		require.False(t, exposeToolAPI(env(map[string]string{"DATAAPI_EXPOSE_TOOL_API": v})), "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		require.True(t, exposeToolAPI(env(map[string]string{"DATAAPI_EXPOSE_TOOL_API": v})), "value %q", v)
	}
}

func TestExposeMCP_FailsClosed(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage", "tru"} {
		require.False(t, exposeMCP(env(map[string]string{"DATAAPI_EXPOSE_MCP": v})), "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		require.True(t, exposeMCP(env(map[string]string{"DATAAPI_EXPOSE_MCP": v})), "value %q", v)
	}
}

func post(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewHandler_ServesV1AndKeepsToolAPIOffByDefault(t *testing.T) {
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{}, slog.Default())
	require.NoError(t, err)

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.NotEmpty(t, health.Header().Get("X-Request-ID"))

	require.Equal(t, http.StatusNotFound, post(h, "/v1/tool/get_jumps_between").Code)
	// The legacy native paths do not exist on this service.
	require.Equal(t, http.StatusNotFound, post(h, "/api/fit/stats").Code)
}

func TestNewHandler_ExposedToolAPIIsPublicMode(t *testing.T) {
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{toolAPI: true}, slog.Default())
	require.NoError(t, err)

	// The tiers apply: this binary is internet-facing and has no API keys here.
	rec := post(h, "/v1/tool/appraise_items")
	require.Equal(t, http.StatusBadRequest, rec.Code, "public: a call without items fails the argument check")
	require.Contains(t, rec.Body.String(), `"invalid_arguments"`)
	require.Equal(t, http.StatusUnauthorized, post(h, "/v1/tool/analyze_battle").Code, "keyed: needs an API key")
	require.NotEqual(t, http.StatusUnauthorized, post(h, "/v1/tool/get_fits").Code, "fit lookups are public")
	require.Equal(t, http.StatusForbidden, post(h, "/v1/tool/convert_isk_to_real").Code, "disabled")
	require.Equal(t, http.StatusNotFound, post(h, "/v1/tool/no_such_tool").Code)
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestNewHandler_ServesTheGeneratedDescriptions(t *testing.T) {
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{}, slog.Default())
	require.NoError(t, err)

	for _, path := range []string{"/v1/openapi.yaml", "/v1/openapi.json", "/llms.txt", "/llms-full.txt"} {
		rec := get(h, path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.NotEmpty(t, rec.Body.Bytes(), path)
	}
	// With nothing exposed the documents promise nothing: no tool operations, no MCP.
	spec := get(h, "/v1/openapi.yaml").Body.String()
	require.NotContains(t, spec, "/tool/")
	require.NotContains(t, spec, "/mcp:")
}

func TestNewHandler_MCPIsOffByDefaultAndPublicSafeWhenOn(t *testing.T) {
	d := &bootstrap.Deps{Tools: &tools.Deps{}}

	off, err := newHandler(d, exposure{}, slog.Default())
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, post(off, "/v1/mcp").Code)

	on, err := newHandler(d, exposure{mcp: true}, slog.Default())
	require.NoError(t, err)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// The tunnel's connector reaches the process over loopback with the public Host.
	req.Host = "api.example.test"
	rec := httptest.NewRecorder()
	on.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("X-Request-ID"))
	require.Contains(t, rec.Body.String(), "get_jumps_between")
	require.Contains(t, rec.Body.String(), `"appraise_items"`, "appraise_items is public")
	for _, denied := range []string{"analyze_battle", "convert_isk_to_real"} {
		require.NotContains(t, rec.Body.String(), `"`+denied+`"`)
	}
	require.Contains(t, get(on, "/v1/openapi.yaml").Body.String(), "/mcp:")
}

func TestLoadAuth(t *testing.T) {
	a, err := loadAuth(env(nil))
	require.NoError(t, err)
	require.Nil(t, a, "unset means no keys, as a nil interface")

	path := t.TempDir() + "/keys.txt"
	require.NoError(t, os.WriteFile(path, []byte("ci:"+dataapi.HashKey("ci-key")+"\n"), 0o600))
	a, err = loadAuth(env(map[string]string{"DATAAPI_API_KEYS_FILE": path}))
	require.NoError(t, err)
	require.NotNil(t, a)

	bad := t.TempDir() + "/bad.txt"
	require.NoError(t, os.WriteFile(bad, []byte("ci-key-in-plaintext\n"), 0o600))
	_, err = loadAuth(env(map[string]string{"DATAAPI_API_KEYS_FILE": bad}))
	require.ErrorContains(t, err, "DATAAPI_API_KEYS_FILE")
	require.NotContains(t, err.Error(), "ci-key-in-plaintext")

	_, err = loadAuth(env(map[string]string{"DATAAPI_API_KEYS_FILE": path + ".missing"}))
	require.Error(t, err)
}

func TestNewHandler_APIKeyUnlocksTheKeyedTierOnRESTAndMCP(t *testing.T) {
	keys, err := dataapi.ParseKeyFile(strings.NewReader("ci:" + dataapi.HashKey("ci-key") + "\n"))
	require.NoError(t, err)
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{toolAPI: true, mcp: true, auth: keys}, slog.Default())
	require.NoError(t, err)

	withKey := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer ci-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	require.NotEqual(t, http.StatusUnauthorized, withKey("/v1/tool/analyze_battle").Code)
	require.NotEqual(t, http.StatusForbidden, withKey("/v1/tool/analyze_battle").Code)

	list := func(hdr ...string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Host = "api.example.test"
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	require.NotContains(t, list(), `"analyze_battle"`)
	require.Contains(t, list(), `"get_fits"`)
	require.Contains(t, list("X-API-Key", "ci-key"), `"analyze_battle"`)

	// The documents describe the keys this process accepts.
	require.Contains(t, get(h, "/v1/openapi.yaml").Body.String(), "securitySchemes")
}

func TestSDEOnly_FailsClosedToConfiguredCorpus(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage", "tru"} {
		require.False(t, sdeOnly(env(map[string]string{"DATAAPI_SDE_ONLY": v})), "value %q", v)
	}
	for _, v := range []string{"1", "true", "TRUE", "t"} {
		require.True(t, sdeOnly(env(map[string]string{"DATAAPI_SDE_ONLY": v})), "value %q", v)
	}
}

func TestPublicURL(t *testing.T) {
	u, err := publicURL(env(nil))
	require.NoError(t, err)
	require.Empty(t, u, "unset keeps relative links")

	u, err = publicURL(env(map[string]string{"DATAAPI_PUBLIC_URL": "https://data.example.test/"}))
	require.NoError(t, err)
	require.Equal(t, "https://data.example.test", u)

	for _, bad := range []string{"http://data.example.test", "https://data.example.test/v1", "https://data.example.test?x=1", "data.example.test", "ftp://x"} {
		_, err = publicURL(env(map[string]string{"DATAAPI_PUBLIC_URL": bad}))
		require.ErrorContains(t, err, "DATAAPI_PUBLIC_URL", bad)
	}
}

func TestNewHandler_PublicURLMakesTheDescriptionsAbsolute(t *testing.T) {
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{publicURL: "https://data.example.test"}, slog.Default())
	require.NoError(t, err)

	require.Contains(t, get(h, "/v1/openapi.yaml").Body.String(), "url: https://data.example.test/v1")
	require.Contains(t, get(h, "/llms.txt").Body.String(), "](https://data.example.test/v1/openapi.yaml)")
	require.NotContains(t, get(h, "/llms.txt").Body.String(), "](/")
}

func TestNewHandler_ServesTheIndexAndTerms(t *testing.T) {
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{}, slog.Default())
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, get(h, "/").Code)
	require.Equal(t, http.StatusOK, get(h, "/terms").Code)
	require.Equal(t, http.StatusOK, get(h, "/terms.md").Code)
}

func TestKeyedFactor(t *testing.T) {
	f, err := keyedFactor(env(nil))
	require.NoError(t, err)
	require.Equal(t, 2, f)
	f, err = keyedFactor(env(map[string]string{"DATAAPI_KEYED_LIMIT_FACTOR": "1"}))
	require.NoError(t, err)
	require.Equal(t, 1, f)
	f, err = keyedFactor(env(map[string]string{"DATAAPI_KEYED_LIMIT_FACTOR": "10"}))
	require.NoError(t, err)
	require.Equal(t, 10, f)
	for _, bad := range []string{"0", "11", "x", "-1"} {
		_, err = keyedFactor(env(map[string]string{"DATAAPI_KEYED_LIMIT_FACTOR": bad}))
		require.ErrorContains(t, err, "DATAAPI_KEYED_LIMIT_FACTOR", bad)
	}
}

func TestDailyCap(t *testing.T) {
	c, err := dailyCap(env(nil))
	require.NoError(t, err)
	require.Equal(t, 10000, c)
	c, err = dailyCap(env(map[string]string{"DATAAPI_KEY_DAILY_CAP": "0"}))
	require.NoError(t, err)
	require.Equal(t, 0, c)
	c, err = dailyCap(env(map[string]string{"DATAAPI_KEY_DAILY_CAP": "250"}))
	require.NoError(t, err)
	require.Equal(t, 250, c)
	c, err = dailyCap(env(map[string]string{"DATAAPI_KEY_DAILY_CAP": "10000000"}))
	require.NoError(t, err)
	require.Equal(t, 10000000, c)
	for _, bad := range []string{"-1", "x", "1.5", "10000001"} {
		_, err = dailyCap(env(map[string]string{"DATAAPI_KEY_DAILY_CAP": bad}))
		require.ErrorContains(t, err, "DATAAPI_KEY_DAILY_CAP", bad)
	}
}

func TestAnonDailyCap(t *testing.T) {
	c, err := anonDailyCap(env(nil))
	require.NoError(t, err)
	require.Equal(t, 200, c, "default: 200 requests a day per client without a key")
	c, err = anonDailyCap(env(map[string]string{"DATAAPI_ANON_DAILY_CAP": "0"}))
	require.NoError(t, err)
	require.Equal(t, 0, c, "0 turns the cap off")
	c, err = anonDailyCap(env(map[string]string{"DATAAPI_ANON_DAILY_CAP": " 50 "}))
	require.NoError(t, err)
	require.Equal(t, 50, c)
	for _, bad := range []string{"-1", "x", "1.5", "10000001"} {
		_, err = anonDailyCap(env(map[string]string{"DATAAPI_ANON_DAILY_CAP": bad}))
		require.ErrorContains(t, err, "DATAAPI_ANON_DAILY_CAP", bad)
	}
}

func TestNewHandler_AnonymousClientsHaveADailyBudgetWhereKeysExist(t *testing.T) {
	store := openStore(t, 0)
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{store: store, anonDailyCap: 2, keyedFactor: 2}, slog.Default())
	require.NoError(t, err)
	for range 2 {
		require.Equal(t, http.StatusNotFound, get(h, "/v1/no/such/route").Code)
	}
	over := get(h, "/v1/no/such/route")
	require.Equal(t, http.StatusTooManyRequests, over.Code)
	require.Contains(t, over.Body.String(), "daily_quota_exceeded")
	require.Equal(t, http.StatusOK, get(h, "/v1/openapi.json").Code, "browsing the docs is never counted or refused")
	require.Contains(t, get(h, "/llms-full.txt").Body.String(), "2 requests per UTC day per client")

	// no keys at all: no cap, and the full numbers for everyone
	h, err = newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{anonDailyCap: 2}, slog.Default())
	require.NoError(t, err)
	for range 5 {
		require.Equal(t, http.StatusNotFound, get(h, "/v1/no/such/route").Code)
	}
	require.NotContains(t, get(h, "/llms-full.txt").Body.String(), "per UTC day per client")
}

func TestSDEBuild(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sde.sqlite"
	log := slog.New(slog.DiscardHandler)
	require.Equal(t, "", sdeBuild(path, log))
	require.NoError(t, os.WriteFile(path+".build", []byte("123_20261007\n"), 0o600))
	require.Equal(t, "123_20261007", sdeBuild(path, log))
	require.Equal(t, "", sdeBuild("", log))
	// The value is served publicly: anything but a plain id, or an oversized file, is dropped.
	for _, bad := range []string{"has space", "<script>", "a/b", "", strings.Repeat("a", 65), strings.Repeat("a", 5000)} {
		require.NoError(t, os.WriteFile(path+".build", []byte(bad), 0o600))
		require.Equal(t, "", sdeBuild(path, log), "%.20q", bad)
	}
	require.NoError(t, os.WriteFile(path+".build", []byte(strings.Repeat("a", 64)), 0o600))
	require.Equal(t, strings.Repeat("a", 64), sdeBuild(path, log))
}

func openStore(t *testing.T, cap int) *keys.Store {
	t.Helper()
	s, err := keys.Open(filepath.Join(t.TempDir(), "keys.db"), keys.Options{DailyCap: cap})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNewHandler_StoreKeysAuthenticateAndHonourTheDailyCap(t *testing.T) {
	store := openStore(t, 2)
	key, secret, err := store.Create(context.Background(), "discord:1", "Tester", "ci")
	require.NoError(t, err)
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{store: store, keyedFactor: 2, dailyCap: 2}, slog.Default())
	require.NoError(t, err)

	call := func(secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusOK, call(secret).Code)
	require.Equal(t, http.StatusOK, call(secret).Code)
	third := call(secret)
	require.Equal(t, http.StatusTooManyRequests, third.Code)
	require.Contains(t, third.Body.String(), "daily_quota_exceeded")
	require.NotEmpty(t, third.Header().Get("Retry-After"))
	// Anonymous callers are not charged.
	require.Equal(t, http.StatusOK, call("").Code)

	// The keyed requests were recorded (the refusal too).
	usage, err := store.Usage(context.Background(), key.ID, 1)
	require.NoError(t, err)
	require.Len(t, usage.Days, 1)
	require.Equal(t, 2, usage.Days[0].Count, "two served requests")
	require.Equal(t, 1, usage.Days[0].Refused, "the quota refusal")
	require.NotEmpty(t, usage.Endpoints)
}

func TestNewHandler_StoreAndFileKeysCoexist(t *testing.T) {
	store := openStore(t, 0)
	_, secret, err := store.Create(context.Background(), "discord:1", "Tester", "")
	require.NoError(t, err)
	file, err := dataapi.ParseKeyFile(strings.NewReader("ci:" + dataapi.HashKey("ci-key") + "\n"))
	require.NoError(t, err)
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{toolAPI: true, auth: file, store: store}, slog.Default())
	require.NoError(t, err)
	for _, k := range []string{"ci-key", secret} {
		req := httptest.NewRequest(http.MethodPost, "/v1/tool/analyze_battle", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+k)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, "past the key gate: the missing argument is the refusal")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/tool/analyze_battle", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer ecd_"+strings.Repeat("0", 64))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAdminServer_OnlyWithStoreAndToken(t *testing.T) {
	store := openStore(t, 0)
	log := slog.Default()
	require.Nil(t, adminServer(nil, "test-admin-token", ":0", log))
	require.Nil(t, adminServer(store, "", ":0", log))
	srv := adminServer(store, "test-admin-token", "127.0.0.1:0", log)
	require.NotNil(t, srv)
	require.Equal(t, "127.0.0.1:0", srv.Addr)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/ops/keys", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the admin API demands its token")
}

func TestAdminAddr(t *testing.T) {
	require.Equal(t, "127.0.0.1:8093", adminAddr(env(nil)), "loopback unless the deployment says otherwise")
	require.Equal(t, "127.0.0.1:9", adminAddr(env(map[string]string{"DATAAPI_ADMIN_ADDR": "127.0.0.1:9"})))
}

func TestNewHandler_DocsPromiseNoCapWithoutAStore(t *testing.T) {
	file, err := dataapi.ParseKeyFile(strings.NewReader("ci:" + dataapi.HashKey("ci-key") + "\n"))
	require.NoError(t, err)
	d := &bootstrap.Deps{Tools: &tools.Deps{}}
	h, err := newHandler(d, exposure{toolAPI: true, auth: file, dailyCap: 10000, keyedFactor: 2}, slog.Default())
	require.NoError(t, err)
	for _, p := range []string{"/v1/openapi.yaml", "/llms-full.txt"} {
		// (the error-code list names daily_quota_exceeded regardless; the cap promise is the thing.)
		require.NotContains(t, get(h, p).Body.String(), "daily cap of", p)
	}
	h, err = newHandler(d, exposure{toolAPI: true, store: openStore(t, 10000), dailyCap: 10000, keyedFactor: 2}, slog.Default())
	require.NoError(t, err)
	require.Contains(t, get(h, "/v1/openapi.yaml").Body.String(), "daily cap of 10000 requests")
}

func mcpCall(h http.Handler, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_jumps_between","arguments":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Host = "api.example.test"
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewHandler_MCPToolCallIsOneRequestPlusASubBreakdown(t *testing.T) {
	store := openStore(t, 0)
	key, secret, err := store.Create(context.Background(), "discord:1", "Tester", "ci")
	require.NoError(t, err)
	h, err := newHandler(&bootstrap.Deps{Tools: &tools.Deps{}}, exposure{mcp: true, store: store}, slog.Default())
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, mcpCall(h, secret).Code)
	require.Equal(t, http.StatusOK, mcpCall(h, "").Code) // anonymous: records nothing

	u, err := store.Usage(context.Background(), key.ID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, u.Days[0].Count, "one MCP POST is one served request")
	got := map[string]int{}
	for _, e := range u.Endpoints {
		got[e.Endpoint] = e.Count
	}
	require.Equal(t, map[string]int{"/v1/mcp": 1, "mcp:get_jumps_between": 1}, got)
}

func TestCheckAdminToken(t *testing.T) {
	require.NoError(t, checkAdminToken(""), "no token: no admin listener, nothing to check")
	require.NoError(t, checkAdminToken(strings.Repeat("a", 32)))
	err := checkAdminToken("short-secret-value")
	require.ErrorContains(t, err, "DATAAPI_ADMIN_TOKEN")
	require.NotContains(t, err.Error(), "short-secret-value", "the value is never in the error")
}

func TestShutdownBudgetFitsDockersStopTimeout(t *testing.T) {
	require.Less(t, shutdownBudget, 10*time.Second)
}
