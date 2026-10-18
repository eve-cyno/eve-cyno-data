package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

// connect wires an in-process client to s.
func connect(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func text(t *testing.T, c mcp.Content) string {
	t.Helper()
	tc, ok := c.(*mcp.TextContent)
	require.True(t, ok, "want text content, got %T", c)
	return tc.Text
}

// fakeExec returns an executor that records the call and answers res/err.
type fakeExec struct {
	res  tools.Result
	err  error
	name string
	args map[string]any
}

func (f *fakeExec) exec(_ context.Context, _ *tools.Deps, name string, args map[string]any) (tools.Result, error) {
	f.name, f.args = name, args
	return f.res, f.err
}

func TestCall_SuccessMapsTextDataAndAttribution(t *testing.T) {
	type payload struct {
		TypeID int    `json:"type_id"`
		Name   string `json:"name"`
	}
	f := &fakeExec{res: tools.Result{
		Tool: "get_jumps_between", Version: "v-test", Text: "Jita to Amarr: 11 jumps",
		Data:        &payload{TypeID: 34, Name: "Tritanium"},
		Attribution: []tools.Source{{Name: "EVE Static Data Export (via Fuzzwork)", URL: "https://www.fuzzwork.co.uk/dump/", License: "CCP data"}},
	}}
	cs := connect(t, newServer(&tools.Deps{}, Options{}, f.exec))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_jumps_between",
		Arguments: map[string]any{"from_system": "Jita", "to_system": "Amarr"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	require.Equal(t, "get_jumps_between", f.name)
	require.Equal(t, map[string]any{"from_system": "Jita", "to_system": "Amarr"}, f.args)

	require.Len(t, res.Content, 2)
	require.Equal(t, "Jita to Amarr: 11 jumps", text(t, res.Content[0]), "content[0] is exactly the tool text")
	require.Equal(t, "Sources: EVE Static Data Export (via Fuzzwork) <https://www.fuzzwork.co.uk/dump/>", text(t, res.Content[1]))

	require.Equal(t, map[string]any{"type_id": float64(34), "name": "Tritanium"}, res.StructuredContent)

	require.Equal(t, "get_jumps_between", res.Meta[MetaPrefix+"tool"])
	require.Equal(t, "v-test", res.Meta[MetaPrefix+"version"])
	attr, ok := res.Meta[MetaPrefix+"attribution"].([]any)
	require.True(t, ok)
	require.Len(t, attr, 1)
	require.Equal(t, "CCP data", attr[0].(map[string]any)["license"])
}

func TestCall_TextOnlyResultHasNoStructuredContent(t *testing.T) {
	f := &fakeExec{res: tools.Result{Tool: "get_npc_stations", Text: "no stations"}}
	cs := connect(t, newServer(&tools.Deps{}, Options{}, f.exec))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_npc_stations", Arguments: map[string]any{"system_name": "Nowhere"}})
	require.NoError(t, err)
	require.Len(t, res.Content, 1, "no upstreams: no Sources line")
	require.Nil(t, res.StructuredContent)
	require.Equal(t, []any{}, res.Meta[MetaPrefix+"attribution"], "attribution is an empty list, never null")
}

func TestCall_NoArgumentsRunsWithEmptyMap(t *testing.T) {
	f := &fakeExec{res: tools.Result{Text: "ok"}}
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true}, f.exec))

	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_fits"}) // no required arguments
	require.NoError(t, err)
	require.NotNil(t, f.args)
	require.Empty(t, f.args)
}

func TestCall_ToolErrorIsAnIsErrorResult(t *testing.T) {
	f := &fakeExec{err: errors.New("esi: 502 bad gateway")}
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true}, f.exec))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_market_price", Arguments: map[string]any{"type_id": 34}})
	require.NoError(t, err, "a tool failure is not a protocol error")
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	require.Equal(t, "esi: 502 bad gateway", text(t, res.Content[0]))
}

func TestCall_TimeoutIsAnIsErrorResult(t *testing.T) {
	slow := func(ctx context.Context, d *tools.Deps, name string, args map[string]any) (tools.Result, error) {
		<-ctx.Done()
		return tools.Result{}, ctx.Err()
	}
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true, Timeout: 20 * time.Millisecond}, slow))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_market_price", Arguments: map[string]any{"type_id": 34}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, text(t, res.Content[0]), "get_market_price timed out after 20ms")
}

func TestCall_PanicIsRecovered(t *testing.T) {
	boom := func(context.Context, *tools.Deps, string, map[string]any) (tools.Result, error) {
		panic("boom")
	}
	cs := connect(t, newServer(&tools.Deps{}, Options{Logger: slog.New(slog.DiscardHandler)}, boom))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_npc_stations", Arguments: map[string]any{"system_name": "Jita"}})
	require.NoError(t, err, "the session survives a panicking tool")
	require.True(t, res.IsError)
	require.Equal(t, "internal error while running get_npc_stations", text(t, res.Content[0]))

	// ... and still answers the next call.
	_, err = cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
}

func TestCall_NilDepsIsAnIsErrorResult(t *testing.T) {
	f := &fakeExec{res: tools.Result{Text: "must not run"}}
	cs := connect(t, newServer(nil, Options{}, f.exec))

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_npc_stations", Arguments: map[string]any{"system_name": "Jita"}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, text(t, res.Content[0]), "unavailable")
	require.Empty(t, f.name, "the executor is not called without deps")
}

func TestCall_ArgumentsMustBeAnObject(t *testing.T) {
	f := &fakeExec{res: tools.Result{Text: "must not run"}}
	cs := connect(t, newServer(&tools.Deps{}, Options{}, f.exec))

	// The typed client always sends an object; send a JSON array through the raw request.
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_npc_stations", Arguments: []string{"Jita"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "arguments must be a JSON object")
	require.Empty(t, f.name)
}

func TestTools_AnnotationsMarkReadOnly(t *testing.T) {
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true}, (&fakeExec{}).exec))
	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)

	byName := map[string]*mcp.Tool{}
	for _, tl := range list.Tools {
		byName[tl.Name] = tl
	}
	jumps := byName["get_jumps_between"]
	require.NotNil(t, jumps)
	require.True(t, jumps.Annotations.ReadOnlyHint)
	require.True(t, jumps.Annotations.IdempotentHint)
	require.False(t, *jumps.Annotations.OpenWorldHint, "an SDE-only tool is closed-world")

	market := byName["get_market_price"]
	require.NotNil(t, market)
	require.True(t, *market.Annotations.OpenWorldHint, "a tool that calls ESI is open-world")
}

func TestMCPTool_EmbeddedDatasetsAreNotOpenWorld(t *testing.T) {
	cs := connect(t, newServer(&tools.Deps{}, Options{All: true}, (&fakeExec{}).exec))
	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	open := map[string]bool{}
	for _, tl := range list.Tools {
		open[tl.Name] = *tl.Annotations.OpenWorldHint
	}
	require.False(t, open["find_systems"], "the ice dataset ships with the binary")
	require.False(t, open["get_system_info"])
	require.True(t, open["plan_jump_route"], "the live options read ESI")
	require.True(t, open["get_market_price"])
	require.True(t, open["get_thera_connections"], "EvE-Scout data is read live")
}

// postRPC sends one JSON-RPC message to h like an MCP client does.
func postRPC(t *testing.T, h http.Handler, body string, hdr ...string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// validArgs satisfies the required arguments of the tools the tests call.
var validArgs = map[string]string{
	"appraise_items":      `{"items":"Tritanium"}`,
	"get_jumps_between":   `{"from_system":"Jita","to_system":"Amarr"}`,
	"get_type_info":       `{"type_id":587}`,
	"get_market_price":    `{"type_id":34}`,
	"get_sovereignty":     `{"system_name":"Jita"}`,
	"get_system_activity": `{"system_name":"Jita"}`,
}

func TestHTTPHandler_PerRequestDepsCarryOnlyTheCallersJaniceKey(t *testing.T) {
	shared := &tools.Deps{JaniceAPIKey: "project-key"}
	var mu sync.Mutex
	var seen []string
	var public []bool
	exec := func(_ context.Context, d *tools.Deps, name string, _ map[string]any) (tools.Result, error) {
		mu.Lock()
		seen = append(seen, name+"="+d.JaniceAPIKey)
		public = append(public, d.Public)
		mu.Unlock()
		return tools.Result{Tool: name, Text: "ok"}, nil
	}
	h := httpHandler(shared, HTTPOptions{DisableLocalhostProtection: true}, exec)
	call := func(name string, hdr ...string) string {
		return postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+validArgs[name]+`}}`, hdr...)
	}

	require.Contains(t, call("appraise_items"), `"text":"ok"`, "no header: the tool runs without a key")
	require.Contains(t, call("appraise_items", "X-Janice-Key", "  mine  "), `"text":"ok"`)
	require.Contains(t, call("get_jumps_between"), `"text":"ok"`)
	require.Contains(t, call("get_jumps_between", "X-Janice-Key", strings.Repeat("k", 300)), `"text":"ok"`, "an overlong key is dropped, not forwarded")

	require.Equal(t, []string{"appraise_items=", "appraise_items=mine", "get_jumps_between=", "get_jumps_between="}, seen,
		"the executor only ever sees the caller's key, never the project's")
	require.Equal(t, []bool{true, true, true, true}, public, "every request runs with the public texts")
	require.Equal(t, "project-key", shared.JaniceAPIKey, "the shared deps are not mutated")
	require.False(t, shared.Public, "the shared deps are not mutated")
}

// The stdio server's default (public) mode never spends a key of the process; All keeps it.
func TestNewServer_PublicModeDropsTheProcessJaniceKey(t *testing.T) {
	deps := &tools.Deps{JaniceAPIKey: "machine-key"}
	var seen []string
	var public []bool
	exec := func(_ context.Context, d *tools.Deps, name string, _ map[string]any) (tools.Result, error) {
		seen = append(seen, name+"="+d.JaniceAPIKey)
		public = append(public, d.Public)
		return tools.Result{Tool: name, Text: "ok"}, nil
	}
	for _, all := range []bool{false, true} {
		cs := connect(t, newServer(deps, Options{All: all}, exec))
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "appraise_items", Arguments: map[string]any{"items": "Tritanium"}})
		require.NoError(t, err)
	}
	require.Equal(t, []string{"appraise_items=", "appraise_items=machine-key"}, seen)
	require.Equal(t, []bool{true, false}, public, "the public mode answers with the public texts, All with the chat's")
	require.Equal(t, "machine-key", deps.JaniceAPIKey, "the caller's deps are not mutated")
}

func TestHTTPHandler_KeyedTierFollowsTheKeyedCallback(t *testing.T) {
	list := func(o HTTPOptions, hdr ...string) string {
		return postRPC(t, httpHandler(&tools.Deps{}, o, nil), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, hdr...)
	}
	o := HTTPOptions{DisableLocalhostProtection: true}
	require.NotContains(t, list(o), `"analyze_battle"`, "no callback: nobody is keyed")

	o.Keyed = func(r *http.Request) bool { return r.Header.Get("X-Test-Keyed") == "yes" }
	require.NotContains(t, list(o), `"analyze_battle"`)
	keyed := list(o, "X-Test-Keyed", "yes")
	require.Contains(t, keyed, `"analyze_battle"`)
	require.Contains(t, keyed, `"appraise_items"`)
	require.NotContains(t, keyed, `"convert_isk_to_real"`)
}

func TestHTTPHandler_ToolCalledFiresOncePerCallWithTheCarryingRequest(t *testing.T) {
	var mu sync.Mutex
	var got []string
	o := HTTPOptions{DisableLocalhostProtection: true, ToolCalled: func(r *http.Request, tool string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, tool+"@"+r.Header.Get("X-Test-Caller"))
	}}
	exec := func(_ context.Context, _ *tools.Deps, name string, _ map[string]any) (tools.Result, error) {
		return tools.Result{Tool: name, Text: "ok"}, nil
	}
	h := httpHandler(&tools.Deps{}, o, exec)
	call := func(name string, hdr ...string) {
		postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+validArgs[name]+`}}`, hdr...)
	}
	postRPC(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "X-Test-Caller", "a")
	call("get_jumps_between", "X-Test-Caller", "a")
	call("get_type_info", "X-Test-Caller", "b")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"get_jumps_between@a", "get_type_info@b"}, got, "tools/list does not fire it")
}

func TestCall_OnlyLiveOptionsSpendTheUpstreamBudget(t *testing.T) {
	jump, ok := catalog.Lookup("plan_jump_route")
	require.True(t, ok)
	asked, ran := 0, 0
	exec := func(context.Context, *tools.Deps, string, map[string]any) (tools.Result, error) {
		ran++
		return tools.Result{}, nil
	}
	cfg := serverConfig{allow: func(string) (bool, time.Duration) { asked++; return true, 0 }}
	cs := connect(t, buildServer(&tools.Deps{}, []catalog.Tool{jump}, cfg, exec))
	call := func(extra map[string]any) {
		args := map[string]any{"origin": "Amamake", "destination": "1DQ1-A", "ship": "Rhea"}
		for k, v := range extra {
			args[k] = v
		}
		require.False(t, callWith(t, cs, "plan_jump_route", args).IsError)
	}

	call(nil)
	call(map[string]any{"live_info": false, "avoid_incursions": false})
	require.Zero(t, asked, "calls without a live option cost no live budget")
	call(map[string]any{"avoid_incursions": true})
	require.Equal(t, 1, asked)
	call(map[string]any{"avoid_incursions": true, "live_info": true})
	require.Equal(t, 2, asked)
	require.Equal(t, 4, ran)
}
