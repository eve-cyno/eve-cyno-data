package dataapi_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// connectorX is a keyless caller inside the ranges that has sent nothing yet.
const connectorX = "160.79.111.200"

// fullMCPCeiling serves okMCP with a range ceiling of 2 MCP requests a minute (DefaultLimits
// otherwise), the anonymous daily cap daily and logs into the returned buffer. Two other
// addresses of the ranges fill the ceiling, and the clock moves on 10 s: the ceiling frees in
// 50 s, while connectorX has its whole per-address budget.
func fullMCPCeiling(t *testing.T, daily int) (*dataapi.API, *testClock, *bytes.Buffer) {
	t.Helper()
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.MCPCeiling = dataapi.Rate{Max: 2, Window: time.Minute}
	limits.Range.DailyCap = daily
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), MCP: okMCP,
		Limits: &limits, AnonDailyCap: daily, Now: clk.now, PublicURL: rangeTierURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound), Logger: log,
	})
	spendOK(t, api, "160.79.104.10", 1)
	spendOK(t, api, "153.61.208.11", 1)
	clk.advance(10 * time.Second)
	buf.Reset()
	return api, clk, buf
}

// rpc is a JSON-RPC 2.0 message with the raw id (omitted when ""), the method and raw params
// (omitted when "").
func rpc(id, method, params string) string {
	s := `{"jsonrpc":"2.0"`
	if id != "" {
		s += `,"id":` + id
	}
	s += `,"method":"` + method + `"`
	if params != "" {
		s += `,"params":` + params
	}
	return s + "}"
}

// A tools/call the full range ceiling refuses is answered as JSON-RPC: HTTP 200, the request's
// id (numeric, zero and string alike), a result with isError and one text that says the service
// is busy for Claude connectors and when to retry, Retry-After kept, and no key hint.
func TestRangeCeilingMCP_ToolsCallIsAnIsErrorResult(t *testing.T) {
	for _, id := range []string{`42`, `0`, `"call-7"`, `""`} {
		t.Run(id, func(t *testing.T) {
			api, _, _ := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
			rec := postMCP(t, api, rpc(id, "tools/call", `{"name":"get_jumps_between","arguments":{}}`), fromIP(connectorX)...)
			requireCeilingToolResult(t, rec, id, "tools/call id "+id)
			require.Equal(t, "50", rec.Header().Get("Retry-After"))
			require.Contains(t, rec.Body.String(), "try again in 50 seconds")
		})
	}
}

// Any other method under the full ceiling (initialize, tools/list, ping, one the server does not
// know) is a JSON-RPC error -32000 with the same text and the id echoed.
func TestRangeCeilingMCP_OtherMethodsAreAJSONRPCError(t *testing.T) {
	cases := []struct{ id, method, params string }{
		{`1`, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-ai","version":"0.1.0"}}`},
		{`"init-1"`, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-ai","version":"0.1.0"}}`},
		{`3`, "tools/list", ""},
		{`"p"`, "ping", ""},
		{`9`, "resources/list", `{}`},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.id, func(t *testing.T) {
			api, _, _ := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
			rec := postMCP(t, api, rpc(c.id, c.method, c.params), fromIP(connectorX)...)
			requireCeilingRPCError(t, rec, c.id, c.method)
			require.Equal(t, "50", rec.Header().Get("Retry-After"))
		})
	}
}

// A notification (no id, or a null id) under the full ceiling gets what the stateless transport
// answers every notification: 202 with no body.
func TestRangeCeilingMCP_NotificationIsAcceptedWithNoBody(t *testing.T) {
	for _, body := range []string{
		rpc("", "notifications/initialized", ""),
		rpc("null", "notifications/cancelled", `{"requestId":1}`),
	} {
		api, _, _ := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
		rec := postMCP(t, api, body, fromIP(connectorX)...)
		require.Equal(t, http.StatusAccepted, rec.Code, body)
		require.Empty(t, rec.Body.String(), body)
		require.Equal(t, "50", rec.Header().Get("Retry-After"), body)
	}
}

// What is not one JSON-RPC 2.0 request or notification keeps the HTTP 429 of the ceiling: a
// batch, a body that does not parse, a message without the version, without a method or with an
// id that is neither a string nor a number, a body over the limit, and every method but POST.
func TestRangeCeilingMCP_NonMessagesKeepTheHTTP429(t *testing.T) {
	api, _, _ := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
	for name, body := range map[string]string{
		"batch":        "[" + listToolsRPC + "]",
		"not json":     `{"jsonrpc":`,
		"no version":   `{"id":1,"method":"tools/list"}`,
		"old version":  `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
		"no method":    `{"jsonrpc":"2.0","id":1,"result":{}}`,
		"object id":    `{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`,
		"bool id":      `{"jsonrpc":"2.0","id":true,"method":"tools/list"}`,
		"number name":  `{"jsonrpc":"2.0","id":1,"method":7}`,
		"over limit":   rpc("1", "tools/call", `{"name":"x","arguments":{"pad":"`+strings.Repeat("a", 64<<10)+`"}}`),
		"empty body":   "",
		"json scalar":  `"tools/list"`,
		"deep nesting": rpc("1", "tools/list", strings.Repeat("[", 1001)+strings.Repeat("]", 1001)),
	} {
		requireRESTCeilingRefusal(t, postMCP(t, api, body, fromIP(connectorX)...), 2, name)
	}
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		requireRESTCeilingRefusal(t, do(api, m, "/v1/mcp", "", fromIP(connectorX)...), 2, m)
		// A JSON-RPC body does not make another method a message of the transport.
		requireRESTCeilingRefusal(t, do(api, m, "/v1/mcp", listToolsRPC, fromIP(connectorX)...), 2, m+" with a message")
	}
}

// A ceiling refusal answered as JSON-RPC spends no unit of the caller's daily cap, whatever the
// message: tools/call, tools/list or a notification.
func TestRangeCeilingMCP_RefusalsSpendNoDailyCap(t *testing.T) {
	const daily = 2 // the ceiling's 2 a minute: the daily cap is the binding limit afterwards
	api, clk, _ := fullMCPCeiling(t, daily)
	call := rpc("5", "tools/call", `{"name":"get_jumps_between","arguments":{}}`)
	for i := range 20 {
		requireCeilingToolResult(t, postMCP(t, api, call, fromIP(connectorX)...), `5`, fmt.Sprintf("tools/call %d", i+1))
		requireCeilingRefusal(t, mcpFrom(t, api, connectorX), fmt.Sprintf("tools/list %d", i+1))
		require.Equal(t, http.StatusAccepted, postMCP(t, api, rpc("", "notifications/initialized", ""), fromIP(connectorX)...).Code)
	}
	clk.advance(time.Minute) // the ceiling empties, the UTC day does not change
	spendOK(t, api, connectorX, daily)
	clk.advance(time.Minute) // and again: only the daily cap can refuse now
	requireError(t, mcpFrom(t, api, connectorX), http.StatusTooManyRequests, "daily_quota_exceeded")
}

// A keyless caller outside the ranges, and a range caller over its own address limit, keep the
// HTTP 429 with the REST body: only the shared ceiling, which a connector cannot escape with a
// key, is answered as JSON-RPC.
func TestRangeCeilingMCP_OwnLimitsKeepTheHTTP429(t *testing.T) {
	api, _, _ := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
	spendOK(t, api, "192.0.2.10", 20)
	requireAddressRefusal(t, postMCP(t, api, rpc("1", "tools/call", `{"name":"get_jumps_between","arguments":{}}`), fromIP("192.0.2.10")...), "outside the ranges")

	limits := dataapi.DefaultLimits()
	limits.Range.MCP = dataapi.Rate{Max: 1, Window: time.Minute}
	own := rangeTierAPI(t, anthropicOutbound, &limits, dataapi.DefaultAnonDailyCap, nil)
	spendOK(t, own, connectorX, 1)
	requireAddressRefusal(t, postMCP(t, own, rpc("1", "tools/call", `{"name":"get_jumps_between","arguments":{}}`), fromIP(connectorX)...), "the address's own limit")
}

// The access line of a refusal answered as JSON-RPC records the status sent (200, 202 for a
// notification) and range_refused naming the ceiling; a served request and an HTTP 429 carry no
// range_refused.
func TestRangeCeilingMCP_AccessLineNamesTheCeiling(t *testing.T) {
	api, _, logs := fullMCPCeiling(t, dataapi.DefaultAnonDailyCap)
	postMCP(t, api, rpc("1", "tools/call", `{"name":"get_jumps_between","arguments":{}}`), fromIP(connectorX)...)
	postMCP(t, api, rpc("", "notifications/initialized", ""), fromIP(connectorX)...)
	postMCP(t, api, "["+listToolsRPC+"]", fromIP(connectorX)...)
	lines := requestLines(t, logs)
	require.Len(t, lines, 3)
	require.EqualValues(t, 200, lines[0]["status"])
	require.Equal(t, "mcp_ceiling", lines[0]["range_refused"])
	require.EqualValues(t, 202, lines[1]["status"])
	require.Equal(t, "mcp_ceiling", lines[1]["range_refused"])
	require.EqualValues(t, 429, lines[2]["status"])
	require.NotContains(t, lines[2], "range_refused")
	for _, l := range lines {
		require.NotContains(t, fmt.Sprint(l), connectorX, "no address in the access line")
	}

	clk := newTestClock()
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	deps := &tools.Deps{Client: tools.WithTransport(offlineTransport{})}
	lim := dataapi.NewRangeUpstreamLimiter(limits.UpstreamTool, limits.Range, nil, clk.now)
	live := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t),
		Limits: &limits, AnonDailyCap: dataapi.DefaultAnonDailyCap, Now: clk.now, PublicURL: rangeTierURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound), Upstream: lim, Logger: log,
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true, Keyed: dataapi.KeyedTier, Upstream: dataapi.UpstreamHook(lim),
		}),
	})
	for _, ip := range rangeAddrs(2) {
		for i := range 15 {
			requireLiveServed(t, liveCall(t, live, ip, i), ip)
		}
	}
	buf.Reset()
	requireLiveCeilingRefusal(t, liveMCP(t, live, connectorX), true, "live ceiling, MCP")
	requireLiveCeilingRefusal(t, liveREST(live, connectorX), false, "live ceiling, REST")
	// A JSON-RPC body on a REST route is no MCP message: the REST 429 stays.
	requireLiveCeilingRefusal(t, do(live, http.MethodPost, "/v1/tool/get_type_info",
		rpcCall("get_type_info", `{"type_id":587}`), fromIP(connectorX)...), false, "live ceiling, REST with a JSON-RPC body")
	lines = requestLines(t, buf)
	require.Len(t, lines, 3)
	require.EqualValues(t, 200, lines[0]["status"])
	require.Equal(t, "live_ceiling", lines[0]["range_refused"])
	require.EqualValues(t, 429, lines[1]["status"])
	require.NotContains(t, lines[1], "range_refused")
	require.EqualValues(t, 429, lines[2]["status"])
	require.NotContains(t, lines[2], "range_refused")
}

// requestLines are the access lines ("request") of the JSON log in buf.
func requestLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range logLines(t, buf) {
		if l["msg"] == "request" {
			out = append(out, l)
		}
	}
	return out
}
