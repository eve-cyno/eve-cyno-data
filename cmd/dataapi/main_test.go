package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/dataapi"
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
	require.Equal(t, http.StatusBadRequest, post(h, "/v1/tool/appraise_items").Code, "byo-key: needs X-Janice-Key")
	require.Equal(t, http.StatusUnauthorized, post(h, "/v1/tool/get_fits").Code, "keyed: needs an API key")
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
	require.Contains(t, rec.Body.String(), `"appraise_items"`, "the byo-key tool is listed for everyone")
	for _, denied := range []string{"get_fits", "list_fits", "get_market_price", "convert_isk_to_real"} {
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
	require.Equal(t, http.StatusOK, withKey("/v1/tool/get_fits").Code)

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
	require.NotContains(t, list(), `"get_fits"`)
	require.Contains(t, list("X-API-Key", "ci-key"), `"get_fits"`)

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
