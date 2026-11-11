package dataapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/tools"
)

// A full gate refuses at once instead of queueing, and a released slot admits the next request.
func TestRouteGate_RefusesWhenFullAndFreesOnRelease(t *testing.T) {
	g := newRouteGate(2, 0, false, nil, tieredLimiter{}, "")
	r1, pool, ok := g.tryAcquire(false)
	require.True(t, ok)
	require.Equal(t, poolWork, pool)
	r2, _, ok := g.tryAcquire(false)
	require.True(t, ok)

	_, pool, ok = g.tryAcquire(false)
	require.False(t, ok, "both slots are held")
	require.Equal(t, poolWork, pool, "the refusal names the pool that is full")

	r1()
	r3, _, ok := g.tryAcquire(false)
	require.True(t, ok, "a freed slot admits the next request")
	r2()
	r3()
}

// The two pools of a split gate are independent: a full upstream pool refuses upstream-class
// requests only, and a full work pool refuses work only. Without a split (no upstream cap, or a
// route that cannot class a request as upstream) an upstream-class request takes a work slot.
func TestRouteGate_UpstreamPoolIsSeparate(t *testing.T) {
	g := newRouteGate(1, 1, false, (*API).mcpWaitsOnUpstream, tieredLimiter{}, "")
	require.True(t, g.split())
	up, pool, ok := g.tryAcquire(true)
	require.True(t, ok)
	require.Equal(t, poolUpstream, pool)
	_, pool, ok = g.tryAcquire(true)
	require.False(t, ok)
	require.Equal(t, poolUpstream, pool)
	work, pool, ok := g.tryAcquire(false)
	require.True(t, ok, "a full upstream pool leaves the work pool free")
	require.Equal(t, poolWork, pool)
	_, pool, ok = g.tryAcquire(false)
	require.False(t, ok)
	require.Equal(t, poolWork, pool)
	up()
	work()

	for _, g := range []*routeGate{
		newRouteGate(1, 0, false, (*API).mcpWaitsOnUpstream, tieredLimiter{}, ""),
		newRouteGate(1, 4, false, nil, tieredLimiter{}, ""),
	} {
		require.False(t, g.split())
		release, pool, ok := g.tryAcquire(true)
		require.True(t, ok)
		require.Equal(t, poolWork, pool, "no split: the upstream class takes a work slot")
		_, _, ok = g.tryAcquire(false)
		require.False(t, ok, "and that slot is the only one")
		release()
	}
}

// Only the counted routes get a gate: the method-specific pattern of every route that is not
// trialFree, and the anyMethod MCP pattern (POST only). Fallbacks, the catch-all and the
// documentation have none, and no cap means no gate at all. Only the four routes that can class
// a request as upstream get an upstream pool, and only when the upstream cap is set.
func TestRegister_GatesTheCountedRoutesOnly(t *testing.T) {
	cfg := Config{Docs: true, ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler(), MaxInFlight: 3, MaxInFlightUpstream: 2}
	a, err := New(cfg)
	require.NoError(t, err)
	for _, p := range []string{
		"GET /v1/fits/search", "POST /v1/fits/detail", "POST /v1/fit/stats", "POST /v1/fit/suggest",
		"GET /v1/items/search", "POST /v1/tool/{name}",
	} {
		g := a.gates[p]
		require.NotNil(t, g, p)
		require.False(t, g.postOnly, p)
		require.Equal(t, 3, cap(g.work), p)
	}
	require.NotNil(t, a.gates["/v1/mcp"])
	require.True(t, a.gates["/v1/mcp"].postOnly, "GET and DELETE on MCP are never gated")
	require.Len(t, a.gates, 7)
	for _, p := range []string{"/v1/fits/search", "/v1/tool/{name}", "GET /v1/health", "GET /docs/access", "GET /llms.txt", "/"} {
		require.Nil(t, a.gates[p], p)
	}
	for _, p := range []string{"/v1/mcp", "POST /v1/tool/{name}", "GET /v1/fits/search", "POST /v1/fits/detail"} {
		require.True(t, a.gates[p].split(), p)
		require.Equal(t, 2, cap(a.gates[p].upstream), p)
	}
	for _, p := range []string{"POST /v1/fit/stats", "POST /v1/fit/suggest", "GET /v1/items/search"} {
		require.False(t, a.gates[p].split(), "%s never waits on a third party", p)
	}

	cfg.MaxInFlightUpstream = 0
	a, err = New(cfg)
	require.NoError(t, err)
	require.Len(t, a.gates, 7)
	for p, g := range a.gates {
		require.False(t, g.split(), "upstream 0: no split on %s", p)
	}

	cfg.MaxInFlight = 0
	cfg.MaxInFlightUpstream = 2
	a, err = New(cfg)
	require.NoError(t, err)
	require.Empty(t, a.gates, "0 is off, whatever the upstream cap")
}

// internalSDE opens the real SDE for an in-package test (skipped without one, failed under
// EVE_REQUIRE_SDE).
func internalSDE(t *testing.T) *sde.SDE {
	t.Helper()
	s, err := sde.Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	if !s.Available() {
		sdetest.Skip(t, "SDE sqlite not usable")
	}
	return s
}

// notFoundTransport answers every outbound request 404 without touching the network.
type notFoundTransport struct{}

func (notFoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: req}, nil
}

// liveFlag returns the live attribute of the only tool_call line in buf (false when the handler
// wrote none, as for a message the SDK rejects before any tool runs).
func liveFlag(t *testing.T, buf *bytes.Buffer) bool {
	t.Helper()
	live := false
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		if m["msg"] == "tool_call" {
			n++
			live, _ = m["live"].(bool)
		}
	}
	require.LessOrEqual(t, n, 1, "one call per request: %s", buf.String())
	return live
}

func postReq(target, body string, hdr ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

// withAuth attaches the authentication result to r as ServeHTTP does.
func withAuth(a *API, r *http.Request) *http.Request {
	if a.auth == nil {
		return r
	}
	caller, err := a.auth.Authenticate(r)
	if err != nil {
		caller = Caller{}
	}
	return r.WithContext(context.WithValue(r.Context(), authKey{}, authState{caller: caller, err: err}))
}

// The peek classes a body exactly as the handler that runs it decides whether the call is live:
// the same decoders (case-sensitive envelope keys like the MCP SDK, encoding/json for the
// arguments, last key wins), the same argument check. The handler's decision is the live flag of
// its usage line; nothing in this test reaches the network.
func TestInflight_ClassificationMatchesTheHandler(t *testing.T) {
	deps := &tools.Deps{SDE: internalSDE(t), Client: tools.WithTransport(notFoundTransport{})}
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	a, err := New(Config{
		Deps: NewDeps(deps, nil), ToolAPI: ToolAPIPublic, Limits: &Limits{}, Logger: log,
		MCP:         mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true, Logger: log}),
		MaxInFlight: 8, MaxInFlightUpstream: 4,
	})
	require.NoError(t, err)

	const route = `"origin":"Jita","destination":"Amarr","ship":"Rhea"`
	type argsCase struct{ name, tool, args string }
	argsCases := []argsCase{
		{"live arg", "plan_jump_route", `{` + route + `,"live_info":true}`},
		{"live arg false", "plan_jump_route", `{` + route + `,"live_info":false}`},
		{"no live arg", "plan_jump_route", `{` + route + `}`},
		{"duplicate key, last true", "plan_jump_route", `{` + route + `,"live_info":false,"live_info":true}`},
		{"duplicate key, last false", "plan_jump_route", `{` + route + `,"live_info":true,"live_info":false}`},
		{"wrong case", "plan_jump_route", `{` + route + `,"Live_Info":true}`},
		{"string, not a boolean", "plan_jump_route", `{` + route + `,"live_info":"true"}`},
		{"nested arguments", "plan_jump_route", `{` + route + `,"arguments":{"live_info":true}}`},
		{"live but a required argument missing", "plan_jump_route", `{"origin":"Jita","ship":"Rhea","live_info":true}`},
		{"whitespace", "plan_jump_route", "{\n\t\"origin\" : \"Jita\" ,\r\n \"destination\":\"Amarr\", \"ship\":\"Rhea\",\n\"avoid_incursions\"\t:\ttrue }"},
		{"production chain with a system", "get_production_chain", `{"item_name":"Rifter","system":"Jita"}`},
		{"production chain, blank system", "get_production_chain", `{"item_name":"Rifter","system":"  "}`},
		{"non-object arguments", "plan_jump_route", `["live_info"]`},
		{"string arguments", "plan_jump_route", `"live_info"`},
		{"null arguments", "plan_jump_route", `null`},
		{"always live", "get_sovereignty", `{"system_name":"Jita"}`},
		{"SDE tool", "search_item_by_name", `{"names":["Tritanium"]}`},
	}

	t.Run("MCP", func(t *testing.T) {
		bodies := map[string]string{}
		for _, c := range argsCases {
			bodies[c.name] = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + c.tool + `","arguments":` + c.args + `}}`
		}
		live := `{` + route + `,"live_info":true}`
		for name, body := range map[string]string{
			"duplicate name, last live":      `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_item_by_name","name":"get_sovereignty","arguments":{}}}`,
			"duplicate name, last SDE":       `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","name":"search_item_by_name","arguments":{"names":["Tritanium"]}}}`,
			"Name instead of name":           `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"Name":"get_sovereignty","arguments":{}}}`,
			"Method instead of method":       `{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"Params instead of params":       `{"jsonrpc":"2.0","id":1,"method":"tools/call","Params":{"name":"get_sovereignty","arguments":{}}}`,
			"Arguments instead of arguments": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"plan_jump_route","Arguments":` + live + `}}`,
			"duplicate arguments, last live": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"plan_jump_route","arguments":{` + route + `},"arguments":` + live + `}}`,
			"duplicate method, last list":    `{"jsonrpc":"2.0","id":1,"method":"tools/call","method":"tools/list","params":{"name":"get_sovereignty","arguments":{}}}`,
			"duplicate method, last call":    `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"no arguments":                   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty"}}`,
			"params not an object":           `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":["get_sovereignty"]}`,
			"name not a string":              `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":7,"arguments":{}}}`,
			"tools/list":                     `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"name":"get_sovereignty"}}`,
			"unknown tool":                   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
			"string id":                      `{"jsonrpc":"2.0","id":"a-1","method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"no id (a notification)":         `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"null id":                        `{"jsonrpc":"2.0","id":null,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"object id":                      `{"jsonrpc":"2.0","id":{},"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"no version":                     `{"id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"version 1.0":                    `{"jsonrpc":"1.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`,
			"trailing garbage":               `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}} garbage`,
			"trailing data":                  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}} {}`,
			"not JSON":                       `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty"`,
			"pretty printed":                 "{\n  \"jsonrpc\": \"2.0\",\n  \"id\": 1,\n  \"method\": \"tools/call\",\n  \"params\": {\n    \"name\": \"get_sovereignty\",\n    \"arguments\": {}\n  }\n}\n",
		} {
			bodies[name] = body
		}
		for name, body := range bodies {
			req := postReq("/v1/mcp", body)
			class := a.mcpWaitsOnUpstream(req, "/v1/mcp")
			rest, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(rest), "%s: the peek leaves the body intact", name)

			buf.Reset()
			a.ServeHTTP(httptest.NewRecorder(), postReq("/v1/mcp", body))
			require.Equal(t, liveFlag(t, &buf), class, "%s: %s", name, body)
		}
	})

	t.Run("REST", func(t *testing.T) {
		for _, c := range append(argsCases, argsCase{"empty body", "get_production_chain", ``}) {
			req := postReq("/v1/tool/"+c.tool, c.args)
			class := a.toolWaitsOnUpstream(req, "POST /v1/tool/{name}")
			rest, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, c.args, string(rest), "%s: the peek leaves the body intact", c.name)

			buf.Reset()
			a.ServeHTTP(httptest.NewRecorder(), postReq("/v1/tool/"+c.tool, c.args))
			require.Equal(t, liveFlag(t, &buf), class, "%s: %s %s", c.name, c.tool, c.args)
		}
	})
}

// A body the handler refuses as too large is work, whatever it says: the handler answers 413 at
// once. A body of exactly the limit is still classed.
func TestInflight_OversizedBodyIsWork(t *testing.T) {
	a, err := New(Config{Deps: NewDeps(&tools.Deps{}, nil), ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler()})
	require.NoError(t, err)
	call := func(size int) string {
		head := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{"system_name":"`
		tail := `"}}}`
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
	}
	require.True(t, a.mcpWaitsOnUpstream(postReq("/v1/mcp", call(toolBodyMax)), "/v1/mcp"))
	require.False(t, a.mcpWaitsOnUpstream(postReq("/v1/mcp", call(toolBodyMax+1)), "/v1/mcp"))

	args := func(size int) string {
		head := `{"origin":"Jita","destination":"Amarr","ship":"Rhea","live_info":true,"x":"`
		tail := `"}`
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
	}
	require.True(t, a.toolWaitsOnUpstream(postReq("/v1/tool/plan_jump_route", args(toolBodyMax)), "POST /v1/tool/{name}"))
	require.False(t, a.toolWaitsOnUpstream(postReq("/v1/tool/plan_jump_route", args(toolBodyMax+1)), "POST /v1/tool/{name}"))
}

// The Janice-key part of the class is the key the executor gets: trimmed like
// catalog.JaniceKeyFrom, absent when blank, and absent when longer than the surface accepts
// (REST answers 400 at once; MCP runs without it). Only a tool that accepts a key cares.
func TestInflight_JaniceKeyClassIsTheKeyTheExecutorGets(t *testing.T) {
	appraise, ok := catalog.Lookup("appraise_items")
	require.True(t, ok && appraise.AcceptsJaniceKey)
	a, err := New(Config{Deps: NewDeps(&tools.Deps{}, nil), ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler()})
	require.NoError(t, err)
	mcpCall := func(tool string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"items":"Tritanium 1","names":["Tritanium"]}}}`
	}
	for key, want := range map[string]bool{
		"":                                      false,
		"   ":                                   false,
		"\t \t":                                 false,
		"k":                                     true,
		"  k  ":                                 true,
		strings.Repeat("k", maxCredentialLen):   true,
		strings.Repeat("k", maxCredentialLen+1): false,
	} {
		var hdr []string
		if key != "" {
			hdr = []string{HeaderJaniceKey, key}
		}
		require.Equal(t, want, a.mcpWaitsOnUpstream(postReq("/v1/mcp", mcpCall("appraise_items"), hdr...), "/v1/mcp"), "MCP key %q", key)
		require.Equal(t, want, a.toolWaitsOnUpstream(postReq("/v1/tool/appraise_items", `{"items":"Tritanium 1"}`, hdr...), "POST /v1/tool/{name}"), "REST key %q", key)
		require.False(t, a.mcpWaitsOnUpstream(postReq("/v1/mcp", mcpCall("search_item_by_name"), hdr...), "/v1/mcp"), "a key means nothing to another tool")
	}

	// The loopback tool API runs on the service's own deps: their Janice key decides, not the
	// caller's header.
	for key, want := range map[string]bool{"": false, "project-key": true} {
		a, err := New(Config{Deps: NewDeps(&tools.Deps{JaniceAPIKey: key}, nil), ToolAPI: ToolAPILoopback})
		require.NoError(t, err)
		require.Equal(t, want, a.toolWaitsOnUpstream(postReq("/v1/tool/appraise_items", `{}`, HeaderJaniceKey, "caller"), "POST /v1/tool/{name}"), "deps key %q", key)
	}
}

// An MCP call the transport refuses on its tier (a keyed tool for an anonymous caller, a
// disabled tool for anyone) answers fast, so it is work even though the tool reads an upstream;
// the same keyed tool for a caller with the keyed allowance is upstream.
func TestInflight_MCPTierRefusalIsWork(t *testing.T) {
	a, err := New(Config{Deps: NewDeps(&tools.Deps{}, nil), ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler()})
	require.NoError(t, err)
	call := func(tool, args string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	}
	keyed := func(r *http.Request, tiers ...catalog.Tier) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), authKey{}, authState{caller: NewCaller("k", tiers...)}))
	}
	battle := call("analyze_battle", `{"battle_url":"https://zkillboard.com/related/30000142/202611111200/"}`)
	isk := call("convert_isk_to_real", `{"isk_amount":1000000000,"currency":"BOTH"}`)

	require.False(t, a.mcpWaitsOnUpstream(postReq("/v1/mcp", battle), "/v1/mcp"), "keyed tool, anonymous")
	require.False(t, a.mcpWaitsOnUpstream(keyed(postReq("/v1/mcp", battle)), "/v1/mcp"), "keyed tool, key without the allowance")
	require.True(t, a.mcpWaitsOnUpstream(keyed(postReq("/v1/mcp", battle), catalog.TierKeyed), "/v1/mcp"), "keyed tool, keyed caller")
	require.False(t, a.mcpWaitsOnUpstream(keyed(postReq("/v1/mcp", isk), catalog.TierKeyed), "/v1/mcp"), "disabled tool")
}

// fitSearcher is a FitSearcher that answers an empty page.
type fitSearcher struct{}

func (fitSearcher) SearchFits(context.Context, rag.FitSearchQuery) (rag.FitSearchResult, error) {
	return rag.FitSearchResult{}, nil
}

// Fit search waits on the embedding exactly when the retriever embeds (a non-empty q, blank or
// not) and the handler lets the request get that far.
func TestInflight_FitSearchClass(t *testing.T) {
	const key = "ecd_test_key_one"
	keys, err := ParseKeyFile(strings.NewReader("k1:" + HashKey(key) + "\n"))
	require.NoError(t, err)
	for _, c := range []struct {
		auth      bool
		retriever bool
		query     string
		key       string
		want      bool
	}{
		{false, true, "", "", false},
		{false, true, "ship=Rifter", "", false},
		{false, true, "q=", "", false},
		{false, true, "q=kite", "", true},
		{false, true, "q=%20", "", true},
		{false, false, "q=kite", "", false},          // 503 unavailable at once
		{false, true, "q=kite&offset=24", "", false}, // 400 at once
		{true, true, "q=kite", "", false},            // 401 api_key_required at once
		{true, true, "q=kite", "ecd_wrong", false},   // 401 invalid_api_key at once
		{true, true, "q=kite", key, true},
		{true, true, "q=%20", "", true}, // blank: no key needed, and the retriever still embeds it
	} {
		cfg := Config{Deps: NewDeps(&tools.Deps{}, nil)}
		if c.retriever {
			cfg.Deps.Retriever = fitSearcher{}
		}
		if c.auth {
			cfg.Auth = keys
		}
		a, err := New(cfg)
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodGet, "/v1/fits/search?"+c.query, nil)
		if c.key != "" {
			r.Header.Set(HeaderAPIKey, c.key)
		}
		require.Equal(t, c.want, a.fitsSearchWaitsOnUpstream(withAuth(a, r), "GET /v1/fits/search"), "%+v", c)
	}
}

// Fit detail waits on Janice only with cost:true and a Janice key in the service's deps, decoded
// as the handler decodes it (the first JSON value of at most fitBodyMax bytes, field names
// matched case-insensitively, last key wins).
func TestInflight_FitDetailClass(t *testing.T) {
	big := func(size int) string {
		head := `{"cost":true,"eft":"`
		tail := `"}`
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
	}
	for key, bodies := range map[string]map[string]bool{
		"": {`{"eft":"[Rifter, x]","cost":true}`: false},
		"project-key": {
			`{"eft":"[Rifter, x]","cost":true}`:  true,
			`{"eft":"[Rifter, x]","Cost":true}`:  true,
			`{"eft":"[Rifter, x]","cost":false}`: false,
			`{"eft":"[Rifter, x]"}`:              false,
			`{"eft":"[Rifter, x]","cost":"yes"}`: false,
			`{"cost":true} trailing`:             true,
			`{"cost":true,"cost":false}`:         false,
			`not json`:                           false,
			big(fitBodyMax):                      true,
			big(fitBodyMax + 1):                  false,
		},
	} {
		a, err := New(Config{Deps: NewDeps(&tools.Deps{JaniceAPIKey: key}, nil)})
		require.NoError(t, err)
		for body, want := range bodies {
			req := postReq("/v1/fits/detail", body)
			require.Equal(t, want, a.fitsDetailWaitsOnUpstream(req, "POST /v1/fits/detail"), "key %q body %.60s (%d bytes)", key, body, len(body))
			rest, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(rest))
		}
	}
	// Without a Janice key the body is never read.
	a, err := New(Config{Deps: NewDeps(&tools.Deps{}, nil)})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/fits/detail", tripReader{t})
	require.False(t, a.fitsDetailWaitsOnUpstream(req, "POST /v1/fits/detail"))
}

// tripReader fails the test when it is read.
type tripReader struct{ t *testing.T }

func (r tripReader) Read([]byte) (int, error) {
	r.t.Error("the body was read")
	return 0, io.EOF
}

// errReader fails every read with err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// A peek that meets a read error hands the handler the bytes it got and then the same error, so
// the handler answers as it would have without the peek; the class is work.
func TestPeekBody_ReplaysTheReadError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", io.MultiReader(strings.NewReader(`{"jsonrpc"`), errReader{io.ErrUnexpectedEOF}))
	got, err := peekBody(req, 100)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, `{"jsonrpc"`, string(got))
	rest, err := io.ReadAll(req.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, `{"jsonrpc"`, string(rest))
	require.NoError(t, req.Body.Close())

	got, err = peekBody(httptest.NewRequest(http.MethodPost, "/v1/mcp", nil), 100)
	require.NoError(t, err)
	require.Nil(t, got, "no body: nothing to peek")

	a, err := New(Config{Deps: NewDeps(&tools.Deps{JaniceAPIKey: "k"}, nil), ToolAPI: ToolAPIPublic, MCP: http.NotFoundHandler()})
	require.NoError(t, err)
	fail := func(body string) io.Reader {
		return io.MultiReader(strings.NewReader(body), errReader{io.ErrUnexpectedEOF})
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sovereignty","arguments":{}}}`
	require.False(t, a.mcpWaitsOnUpstream(httptest.NewRequest(http.MethodPost, "/v1/mcp", fail(call)), "/v1/mcp"))
	require.False(t, a.toolWaitsOnUpstream(httptest.NewRequest(http.MethodPost, "/v1/tool/plan_jump_route",
		fail(`{"origin":"Jita","destination":"Amarr","ship":"Rhea","live_info":true}`)), "POST /v1/tool/{name}"))
	require.False(t, a.fitsDetailWaitsOnUpstream(httptest.NewRequest(http.MethodPost, "/v1/fits/detail", fail(`{"cost":true}`)), "POST /v1/fits/detail"))
}

// The backstop: a live call that runs on a request the gate admitted to the work pool is logged
// (tool and surface only) and still served, on MCP and on REST. A live call in the upstream
// pool, or on a gate without a split, logs nothing.
func TestInflight_MisclassifiedLiveCallIsLoggedNotRefused(t *testing.T) {
	never := func(*API, *http.Request, string) bool { return false }
	newAPIWith := func(t *testing.T, upstream int, misclassify bool) (*API, *bytes.Buffer) {
		t.Helper()
		deps := &tools.Deps{Client: tools.WithTransport(notFoundTransport{})}
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		lim := NewUpstreamLimiter(Rate{Max: 100, Window: time.Minute}, nil)
		a, err := New(Config{
			Deps: NewDeps(deps, nil), ToolAPI: ToolAPIPublic, Limits: &Limits{}, Logger: log, Upstream: lim,
			MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
				DisableLocalhostProtection: true, Logger: log, Upstream: UpstreamHook(lim),
			}),
			MaxInFlight: 2, MaxInFlightUpstream: upstream,
		})
		require.NoError(t, err)
		if misclassify {
			for _, g := range a.gates {
				if g.classify != nil {
					g.classify = never // the peek and the handler disagree
				}
			}
		}
		return a, &buf
	}
	misclassified := func(buf *bytes.Buffer) []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "inflight_misclassified" {
				out = append(out, m)
			}
		}
		return out
	}
	mcpBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_type_info","arguments":{"type_id":587}}}`

	a, buf := newAPIWith(t, 1, true)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, postReq("/v1/mcp", mcpBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "rate_limited", "the backstop never refuses")
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, postReq("/v1/tool/get_type_info", `{"type_id":587}`))
	require.NotContains(t, []int{http.StatusServiceUnavailable, http.StatusTooManyRequests}, rec.Code, rec.Body.String())

	lines := misclassified(buf)
	require.Len(t, lines, 2, buf.String())
	for i, surface := range []string{"mcp", "rest"} {
		require.Equal(t, "WARN", lines[i]["level"])
		require.Equal(t, "get_type_info", lines[i]["tool"])
		require.Equal(t, surface, lines[i]["surface"])
		require.NotEmpty(t, lines[i]["request_id"])
		require.Len(t, lines[i], 6, "time, level, msg, request_id, tool, surface: %v", lines[i])
	}

	for name, c := range map[string]struct {
		upstream    int
		misclassify bool
	}{"classed upstream": {1, false}, "no split": {0, true}} {
		a, buf := newAPIWith(t, c.upstream, c.misclassify)
		a.ServeHTTP(httptest.NewRecorder(), postReq("/v1/mcp", mcpBody))
		a.ServeHTTP(httptest.NewRecorder(), postReq("/v1/tool/get_type_info", `{"type_id":587}`))
		require.Empty(t, misclassified(buf), name)
		require.Contains(t, buf.String(), `"live":true`, "%s: the live calls ran", name)
	}
}
