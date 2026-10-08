package dataapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// tieredMCP mounts the MCP handler the way cmd/dataapi does, over a deps whose Janice
// transport is recorded, with the project Janice key set (which must never be used).
func tieredMCP(t *testing.T) (*dataapi.API, *recordingTransport) {
	t.Helper()
	rt := &recordingTransport{}
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.WithTransport(rt), JaniceAPIKey: "project-janice-key"}
	api := newAPI(t, dataapi.Config{
		Deps:    dataapi.NewDeps(deps, nil),
		ToolAPI: dataapi.ToolAPIPublic,
		Auth:    testKeys(t),
		Limits:  &dataapi.Limits{},
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true,
			Keyed:                      func(r *http.Request) bool { return dataapi.CallerFrom(r.Context()).Allows(catalog.TierKeyed) },
		}),
	})
	return api, rt
}

func rpcResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var env struct {
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	require.Nil(t, env.Error, rec.Body.String())
	return env.Result
}

func listedTools(t *testing.T, api http.Handler, hdr ...string) []string {
	t.Helper()
	res := rpcResult(t, postMCP(t, api, listToolsRPC, hdr...))
	var names []string
	for _, tl := range res["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	return names
}

func names(tiers ...catalog.Tier) []string {
	var out []string
	for _, tl := range catalog.ToolsIn(tiers...) {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

func TestMCPTiers_ToolsListIsChosenPerCaller(t *testing.T) {
	api, _ := tieredMCP(t)

	anonymous := names(catalog.TierPublic, catalog.TierBYOKey)
	keyed := names(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey)

	require.Equal(t, anonymous, listedTools(t, api))
	require.Equal(t, keyed, listedTools(t, api, "Authorization", "Bearer "+keyAlpha))
	require.Equal(t, keyed, listedTools(t, api, "X-API-Key", keyBeta))
	require.Equal(t, anonymous, listedTools(t, api), "a keyed caller's list does not leak into the next anonymous one")
	for _, list := range [][]string{anonymous, keyed} {
		require.NotContains(t, list, "convert_isk_to_real", "a disabled tool is never listed")
	}
	require.NotContains(t, anonymous, "get_fits")
	require.Contains(t, anonymous, "appraise_items", "the byo-key tool is listed for everyone")
}

func TestMCPTiers_InvalidKeyIs401NotAnAnonymousList(t *testing.T) {
	api, _ := tieredMCP(t)
	rec := postMCP(t, api, listToolsRPC, "Authorization", "Bearer stale-key")
	requireError(t, rec, http.StatusUnauthorized, "invalid_api_key")
}

func callTool(t *testing.T, api http.Handler, name string, args map[string]any, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	require.NoError(t, err)
	return postMCP(t, api, string(body), hdr...)
}

func TestMCPTiers_KeyedToolIsUnknownWithoutAKey(t *testing.T) {
	api, _ := tieredMCP(t)
	rec := callTool(t, api, "get_fits", map[string]any{})
	require.Contains(t, rec.Body.String(), "unknown tool")

	rec = callTool(t, api, "convert_isk_to_real", map[string]any{}, "X-API-Key", keyAlpha)
	require.Contains(t, rec.Body.String(), "unknown tool")
}

func TestMCPTiers_ByoKeyCallWithoutHeaderIsAnErrorResultNamingIt(t *testing.T) {
	api, rt := tieredMCP(t)
	res := rpcResult(t, callTool(t, api, "appraise_items", map[string]any{"items": "Tritanium 1"}))
	require.Equal(t, true, res["isError"])
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "X-Janice-Key")
	require.Empty(t, rt.janiceKeys(), "no Janice request is made, least of all with the project key")
}

func TestMCPTiers_ByoKeyCallUsesTheCallersKeyOnly(t *testing.T) {
	api, rt := tieredMCP(t)
	res := rpcResult(t, callTool(t, api, "appraise_items", map[string]any{"items": "Tritanium 1"}, "X-Janice-Key", "mcp-caller-key"))
	require.NotEqual(t, true, res["isError"], fmt.Sprint(res))
	require.Equal(t, []string{"mcp-caller-key"}, rt.janiceKeys())

	// The tool list tells a client where the key goes.
	list := rpcResult(t, postMCP(t, api, listToolsRPC))
	for _, tl := range list["tools"].([]any) {
		m := tl.(map[string]any)
		if m["name"] == "appraise_items" {
			require.Contains(t, m["description"], "X-Janice-Key")
		}
	}
}

// Many callers at once, each with its own Janice key: every upstream request carries the
// key of the call that made it, and the shared deps keep the project key (run with -race).
func TestMCPTiers_ConcurrentCallersNeverShareAJaniceKey(t *testing.T) {
	api, rt := tieredMCP(t)
	const n = 24
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("caller-%02d", i)
			res := rpcResult(t, callTool(t, api, "appraise_items", map[string]any{"items": "Tritanium 1"}, "X-Janice-Key", key))
			require.NotEqual(t, true, res["isError"])
		}()
	}
	wg.Wait()

	got := rt.janiceKeys()
	slices.Sort(got)
	var want []string
	for i := range n {
		want = append(want, fmt.Sprintf("caller-%02d", i))
	}
	require.Equal(t, want, got, "one upstream request per call, each with its own caller's key")
	for _, k := range got {
		require.False(t, strings.Contains(k, "project"), k)
	}
}

func TestMCPTiers_ClientSessionSeesKeyedToolsWithAKey(t *testing.T) {
	api, _ := tieredMCP(t)
	ts := httptest.NewServer(api)
	t.Cleanup(ts.Close)

	client := &http.Client{Transport: headerTransport{"X-API-Key": keyAlpha}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/v1/mcp", HTTPClient: client}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	var got []string
	for _, tl := range list.Tools {
		got = append(got, tl.Name)
	}
	slices.Sort(got)
	require.Equal(t, names(catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey), got)
}

type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPTiers_JaniceKeyNeverAppearsInLogs(t *testing.T) {
	const secret = "fake_janice_aaaaaaaaaaaa"
	rt := &recordingTransport{}
	log, buf := logSink()
	deps := &tools.Deps{SDE: realSDE(t), Client: tools.WithTransport(rt)}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), Limits: &dataapi.Limits{}, Logger: log,
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true, Logger: log}),
	})
	rec := callTool(t, api, "appraise_items", map[string]any{"items": "Tritanium 1"}, "X-Janice-Key", secret, "X-API-Key", keyAlpha)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), secret)
	require.NotEmpty(t, buf.String())
	require.NotContains(t, buf.String(), secret)
	require.NotContains(t, buf.String(), keyAlpha)
}
