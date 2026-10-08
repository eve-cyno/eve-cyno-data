package dataapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/tools"
)

const (
	keyAlpha = "alpha-secret-key-0001"
	keyBeta  = "beta-secret-key-0002"
)

func keyFile(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func testKeys(t *testing.T) *dataapi.StaticKeys {
	t.Helper()
	k, err := dataapi.ParseKeyFile(strings.NewReader(keyFile(
		"# self-hosting keys: id:sha256hex",
		"alpha:"+dataapi.HashKey(keyAlpha),
		"",
		"beta:"+strings.ToUpper(dataapi.HashKey(keyBeta)),
	)))
	require.NoError(t, err)
	return k
}

// --- key file --------------------------------------------------------------------

func TestParseKeyFile_AcceptsHashesCommentsAndBlankLines(t *testing.T) {
	k := testKeys(t)
	require.Equal(t, 2, k.Len())
}

func TestParseKeyFile_RejectsBadLinesWithoutEchoingThem(t *testing.T) {
	good := "alpha:" + dataapi.HashKey(keyAlpha)
	cases := map[string]string{
		"plaintext key, no id":      keyAlpha,
		"id with a plaintext key":   "alpha:" + keyAlpha,
		"short hash":                "alpha:abcdef",
		"non-hex hash":              "alpha:" + strings.Repeat("z", 64),
		"empty id":                  ":" + dataapi.HashKey(keyAlpha),
		"id with a space":           "al pha:" + dataapi.HashKey(keyAlpha),
		"id over 64 characters":     strings.Repeat("a", 65) + ":" + dataapi.HashKey(keyAlpha),
		"duplicate id":              good + "\n" + "alpha:" + dataapi.HashKey(keyBeta),
		"duplicate hash":            good + "\n" + "beta:" + dataapi.HashKey(keyAlpha),
		"no keys at all":            "# only a comment\n\n",
		"extra field after hash":    good + ":extra",
		"line over the scanner cap": "alpha:" + strings.Repeat("a", 5000),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := dataapi.ParseKeyFile(strings.NewReader(content + "\n"))
			require.Error(t, err)
			require.NotContains(t, err.Error(), keyAlpha, "a plaintext key must never be echoed")
		})
	}
	_, err := dataapi.ParseKeyFile(strings.NewReader("# fine\n" + good + "\nbroken line\n"))
	require.ErrorContains(t, err, "line 3")
}

func TestLoadKeyFile(t *testing.T) {
	path := t.TempDir() + "/keys.txt"
	require.NoError(t, writeFile(path, keyFile("alpha:"+dataapi.HashKey(keyAlpha))))
	k, err := dataapi.LoadKeyFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, k.Len())

	_, err = dataapi.LoadKeyFile(path + ".missing")
	require.Error(t, err)
}

// --- credentials -------------------------------------------------------------------

func authReq(hdr ...string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/tool/x", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return req
}

func TestStaticKeys_Authenticate(t *testing.T) {
	k := testKeys(t)

	for name, req := range map[string]*http.Request{
		"bearer":            authReq("Authorization", "Bearer "+keyAlpha),
		"bearer lowercase":  authReq("Authorization", "bearer "+keyAlpha),
		"X-API-Key":         authReq("X-API-Key", keyAlpha),
		"bearer wins":       authReq("Authorization", "Bearer "+keyAlpha, "X-API-Key", "junk"),
		"uppercase-hash id": authReq("X-API-Key", keyBeta),
	} {
		t.Run(name, func(t *testing.T) {
			c, err := k.Authenticate(req)
			require.NoError(t, err)
			require.True(t, c.Authenticated())
			require.True(t, c.Allows(catalog.TierKeyed))
		})
	}
	c, err := k.Authenticate(authReq("X-API-Key", keyBeta))
	require.NoError(t, err)
	require.Equal(t, "beta", c.ID)

	// No credential at all, or a non-bearer Authorization scheme: anonymous, not an error.
	for _, req := range []*http.Request{authReq(), authReq("Authorization", "Basic dXNlcjpwYXNz")} {
		c, err := k.Authenticate(req)
		require.NoError(t, err)
		require.False(t, c.Authenticated())
		require.False(t, c.Allows(catalog.TierKeyed))
		require.True(t, c.Allows(catalog.TierPublic))
		require.True(t, c.Allows(catalog.TierBYOKey))
		require.False(t, c.Allows(catalog.TierDisabled))
	}

	// A key that is presented and not accepted is an error, whatever its shape.
	for _, req := range []*http.Request{
		authReq("Authorization", "Bearer wrong"),
		authReq("Authorization", "Bearer "),
		authReq("X-API-Key", "wrong"),
		authReq("X-API-Key", strings.Repeat("k", 300)),
		authReq("Authorization", "Bearer "+dataapi.HashKey(keyAlpha)), // the hash is not the key
	} {
		_, err := k.Authenticate(req)
		require.ErrorIs(t, err, dataapi.ErrInvalidAPIKey)
	}
}

// --- REST tier enforcement -------------------------------------------------------------

// recordingTransport answers every upstream request with an empty 200 and remembers the
// Janice key each request carried.
type recordingTransport struct {
	mu    sync.Mutex
	janKy []string
	urls  []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.janKy = append(r.janKy, req.Header.Get("X-ApiKey"))
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func (r *recordingTransport) janiceKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for i, u := range r.urls {
		if strings.Contains(u, "janice.e-351.com") {
			out = append(out, r.janKy[i])
		}
	}
	return out
}

func tierAPI(t *testing.T, mode dataapi.ToolAPIMode, auth dataapi.Authenticator) (*dataapi.API, *recordingTransport) {
	t.Helper()
	rt := &recordingTransport{}
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.WithTransport(rt), JaniceAPIKey: "project-janice-key"}
	return newAPI(t, dataapi.Config{Deps: dataapi.NewDeps(deps, nil), ToolAPI: mode, Auth: auth, Limits: &dataapi.Limits{}}), rt
}

// gateCodes are the error codes only the tier gate produces.
var gateCodes = []string{"api_key_required", "invalid_api_key", "key_not_permitted", "janice_key_required", "tool_disabled"}

func gateCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code < 400 {
		return ""
	}
	for _, c := range gateCodes {
		if strings.Contains(rec.Body.String(), `"`+c+`"`) {
			return c
		}
	}
	return ""
}

func TestToolTiers_RESTEnforcementMatrix(t *testing.T) {
	api, _ := tierAPI(t, dataapi.ToolAPIPublic, testKeys(t))

	type caller struct {
		name string
		hdr  []string
	}
	anon := caller{"anonymous", nil}
	bearer := caller{"valid bearer key", []string{"Authorization", "Bearer " + keyAlpha}}
	xkey := caller{"valid X-API-Key", []string{"X-API-Key", keyAlpha}}
	bad := caller{"invalid key", []string{"Authorization", "Bearer nope"}}
	janice := caller{"janice header only", []string{"X-Janice-Key", "caller-janice"}}
	keyAndJanice := caller{"key and janice header", []string{"X-API-Key", keyAlpha, "X-Janice-Key", "caller-janice"}}

	// want is the gate code the call must end in; "" means the gate lets it through.
	want := map[catalog.Tier]map[string]string{
		catalog.TierPublic: {
			anon.name: "", bearer.name: "", xkey.name: "", bad.name: "invalid_api_key", janice.name: "", keyAndJanice.name: "",
		},
		catalog.TierKeyed: {
			anon.name: "api_key_required", bearer.name: "", xkey.name: "", bad.name: "invalid_api_key",
			janice.name: "api_key_required", keyAndJanice.name: "",
		},
		catalog.TierBYOKey: {
			anon.name: "janice_key_required", bearer.name: "janice_key_required", xkey.name: "janice_key_required",
			bad.name: "invalid_api_key", janice.name: "", keyAndJanice.name: "",
		},
		catalog.TierDisabled: {
			anon.name: "tool_disabled", bearer.name: "tool_disabled", xkey.name: "tool_disabled",
			bad.name: "invalid_api_key", janice.name: "tool_disabled", keyAndJanice.name: "tool_disabled",
		},
	}
	status := map[string]int{
		"api_key_required": 401, "invalid_api_key": 401, "janice_key_required": 400, "tool_disabled": 403,
	}

	for _, tl := range catalog.Tools() {
		for _, c := range []caller{anon, bearer, xkey, bad, janice, keyAndJanice} {
			t.Run(fmt.Sprintf("%s/%s/%s", tl.Tier, tl.Name, c.name), func(t *testing.T) {
				rec := do(api, "POST", "/v1/tool/"+tl.Name, `{}`, c.hdr...)
				code := want[tl.Tier][c.name]
				if code == "" {
					require.Empty(t, gateCode(t, rec), "the gate must let this through; got %d: %s", rec.Code, rec.Body.String())
					return
				}
				requireError(t, rec, status[code], code)
			})
		}
	}
}

func TestToolTiers_MatrixCoversEveryTier(t *testing.T) {
	// Guard: the matrix above covers every tier that exists today.
	seen := map[catalog.Tier]bool{}
	for _, tl := range catalog.Tools() {
		seen[tl.Tier] = true
	}
	for _, tier := range []catalog.Tier{catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey, catalog.TierDisabled} {
		require.True(t, seen[tier], "no tool in tier %s: the matrix does not exercise it", tier)
	}
}

func TestToolTiers_ServiceWithoutKeysRefusesKeyedTools(t *testing.T) {
	api, _ := tierAPI(t, dataapi.ToolAPIPublic, nil)
	requireError(t, do(api, "POST", "/v1/tool/get_fits", `{}`), http.StatusUnauthorized, "api_key_required")
	// A key header is not an error when there is nothing to check it against.
	requireError(t, do(api, "POST", "/v1/tool/get_fits", `{}`, "X-API-Key", keyAlpha), http.StatusUnauthorized, "api_key_required")
	require.Empty(t, gateCode(t, do(api, "POST", "/v1/tool/get_jumps_between", `{}`)))
}

func TestToolTiers_KeyWithoutTheKeyedAllowanceIsForbidden(t *testing.T) {
	api := newAPI(t, dataapi.Config{
		Deps:    emptyToolDeps(),
		ToolAPI: dataapi.ToolAPIPublic,
		Auth:    fixedAuth{c: dataapi.NewCaller("limited")}, // authenticated, no keyed allowance
	})
	requireError(t, do(api, "POST", "/v1/tool/get_fits", `{}`, "X-API-Key", "whatever"), http.StatusForbidden, "key_not_permitted")
}

type fixedAuth struct{ c dataapi.Caller }

func (f fixedAuth) Authenticate(*http.Request) (dataapi.Caller, error) { return f.c, nil }

func TestToolTiers_LoopbackIgnoresTiersAndKeys(t *testing.T) {
	api, rt := tierAPI(t, dataapi.ToolAPILoopback, testKeys(t))
	for _, name := range []string{"get_fits", "convert_isk_to_real", "get_market_price"} {
		require.Empty(t, gateCode(t, do(api, "POST", "/v1/tool/"+name, `{}`)), name)
	}
	// The user's own machine uses the user's own Janice key.
	rec := do(api, "POST", "/v1/tool/appraise_items", `{"items":"Tritanium 1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"project-janice-key"}, rt.janiceKeys())
}

// --- the Janice key is the caller's, and only for the request -----------------------------

func TestJaniceKey_IsTheCallersAndNeverTheProjects(t *testing.T) {
	api, rt := tierAPI(t, dataapi.ToolAPIPublic, testKeys(t))

	rec := do(api, "POST", "/v1/tool/appraise_items", `{"items":"Tritanium 1"}`, "X-Janice-Key", "caller-one")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(api, "POST", "/v1/tool/appraise_items", `{"items":"Tritanium 1"}`, "X-Janice-Key", "caller-two")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"caller-one", "caller-two"}, rt.janiceKeys())

	// No call on the public surface falls back to the project key.
	requireError(t, do(api, "POST", "/v1/tool/appraise_items", `{"items":"x"}`), http.StatusBadRequest, "janice_key_required")

	// A keyed tool that prices a fit would otherwise reach for the project key.
	for _, k := range rt.janiceKeys() {
		require.NotEqual(t, "project-janice-key", k)
	}
}

func TestJaniceKey_OverlongHeaderIsRefused(t *testing.T) {
	api, _ := tierAPI(t, dataapi.ToolAPIPublic, nil)
	rec := do(api, "POST", "/v1/tool/appraise_items", `{}`, "X-Janice-Key", strings.Repeat("k", 300))
	requireError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestJaniceKey_NeverAppearsInLogsOrResponses(t *testing.T) {
	const secret = "janice-secret-do-not-log-12345"
	log, buf := logSink()
	rt := &recordingTransport{}
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.WithTransport(rt)}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), Logger: log,
	})

	calls := []struct {
		path string
		hdr  []string
	}{
		{"/v1/tool/appraise_items", []string{"X-Janice-Key", secret}},
		{"/v1/tool/get_jumps_between", []string{"X-Janice-Key", secret}},
		{"/v1/tool/get_fits", []string{"X-Janice-Key", secret}},                              // refused: 401
		{"/v1/tool/convert_isk_to_real", []string{"X-Janice-Key", secret}},                   // refused: 403
		{"/v1/tool/get_fits", []string{"X-Janice-Key", secret, "X-API-Key", keyAlpha}},       // runs
		{"/v1/tool/get_fits", []string{"X-Janice-Key", secret, "Authorization", "Bearer x"}}, // invalid key
	}
	for _, c := range calls {
		rec := do(api, "POST", c.path, `{"items":"Tritanium 1"}`, c.hdr...)
		require.NotContains(t, rec.Body.String(), secret, c.path)
		require.NotContains(t, rec.Body.String(), keyAlpha, c.path)
		for k, vs := range rec.Header() {
			require.NotContains(t, strings.Join(vs, ","), secret, "response header %s", k)
		}
	}
	require.NotEmpty(t, buf.String(), "the access log ran")
	require.NotContains(t, buf.String(), secret, "the Janice key is never logged")
	require.NotContains(t, buf.String(), keyAlpha, "an API key is never logged")
	require.NotContains(t, buf.String(), "Bearer", "the Authorization header is never logged")
}

// --- rate-limit bucket per key id -------------------------------------------------------

func TestRateLimit_BucketIsTheKeyIDForAuthenticatedCallers(t *testing.T) {
	api := newAPI(t, dataapi.Config{
		Deps:    emptyToolDeps(),
		ToolAPI: dataapi.ToolAPIPublic,
		Auth:    testKeys(t),
		Limits:  &dataapi.Limits{Tool: dataapi.Rate{Max: 2, Window: time.Minute}},
	})
	call := func(addr string, hdr ...string) int {
		return doFrom(api, addr, "POST", "/v1/tool/no_such_tool", `{}`, hdr...).Code
	}
	alpha := []string{"X-API-Key", keyAlpha}
	beta := []string{"X-API-Key", keyBeta}

	// One key, three different IPs: one shared bucket.
	require.Equal(t, http.StatusNotFound, call("198.51.100.1:1", alpha...))
	require.Equal(t, http.StatusNotFound, call("198.51.100.2:1", alpha...))
	require.Equal(t, http.StatusTooManyRequests, call("198.51.100.3:1", alpha...))

	// Another key has its own budget, and so does the anonymous caller on a fresh IP.
	require.Equal(t, http.StatusNotFound, call("198.51.100.1:1", beta...))
	require.Equal(t, http.StatusNotFound, call("198.51.100.1:1"))
	require.Equal(t, http.StatusNotFound, call("198.51.100.1:1"))
	require.Equal(t, http.StatusTooManyRequests, call("198.51.100.1:1"), "anonymous stays per IP")

	// An invalid key is refused before it can use a key's bucket, and counts against its IP.
	require.Equal(t, http.StatusUnauthorized, call("198.51.100.9:1", "X-API-Key", "nope"))
}

func TestCallerKeys_FallsBackToTheClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:1"
	require.Equal(t, "203.0.113.7", dataapi.CallerKeys{}.Key(r), "no caller in the context: the client IP")
}
