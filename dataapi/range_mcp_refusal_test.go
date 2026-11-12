package dataapi_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
// is busy for Claude connectors and when to retry and ends with the key hint, Retry-After kept.
func TestRangeCeilingMCP_ToolsCallIsAnIsErrorResult(t *testing.T) {
	for _, id := range []string{`42`, `0`, `"call-7"`, `""`, `9223372036854775807`, `-9223372036854775808`} {
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
// id that is neither a string nor an exact int64 (1.5 and 2^63 would be echoed wrongly), a body over the limit, and every method but POST.
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
		"fraction id":  `{"jsonrpc":"2.0","id":1.5,"method":"tools/list"}`,
		"exp id":       `{"jsonrpc":"2.0","id":1e2,"method":"tools/list"}`,
		"float zero":   `{"jsonrpc":"2.0","id":1.0,"method":"tools/list"}`,
		"int64 over":   `{"jsonrpc":"2.0","id":9223372036854775808,"method":"tools/list"}`,
		"huge id":      `{"jsonrpc":"2.0","id":99999999999999999999,"method":"tools/list"}`,
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
	requireDailyRefusal(t, mcpFrom(t, api, connectorX), "the cap is spent by the served requests alone")
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

// fullAddress serves okMCP with a per-address range limit of 2 MCP requests a minute (the shared
// ceiling of DefaultLimits has room), the daily cap daily for every keyless caller and logs into
// the returned buffer. connectorX spends its 2 and the clock moves on 10 s: its own limit frees in
// 50 s. publicURL is the API's PublicURL.
func fullAddress(t *testing.T, daily int, publicURL string) (*dataapi.API, *testClock, *bytes.Buffer) {
	t.Helper()
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.MCP = dataapi.Rate{Max: 2, Window: time.Minute}
	limits.Range.DailyCap = daily
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), MCP: okMCP,
		Limits: &limits, AnonDailyCap: daily, Now: clk.now, PublicURL: publicURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound), Logger: log,
	})
	spendOK(t, api, connectorX, 2)
	clk.advance(10 * time.Second)
	buf.Reset()
	return api, clk, buf
}

// spentDaily serves okMCP with the default range limits and a daily cap of 2 for every keyless
// caller, and logs into the returned buffer; connectorX has spent its cap at 10:00 UTC, 14 hours
// before it resets.
func spentDaily(t *testing.T, publicURL string) (*dataapi.API, *testClock, *bytes.Buffer) {
	t.Helper()
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.DailyCap = 2
	log, buf := logSink()
	api := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), MCP: okMCP,
		Limits: &limits, AnonDailyCap: 2, Now: clk.now, PublicURL: publicURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound), Logger: log,
	})
	spendOK(t, api, connectorX, 2)
	buf.Reset()
	return api, clk, buf
}

const jumpsCall = `{"name":"get_jumps_between","arguments":{}}`

// A range caller over its own per-address MCP limit gets the answer the ceiling gives: a
// tools/call is an isError result, any other method the error -32000, both with the id echoed and
// the busy text with the address's own wait and the key hint; a notification is 202 with no body.
func TestRangeAddressMCP_RefusalIsJSONRPC(t *testing.T) {
	for _, id := range []string{`42`, `"call-7"`} {
		t.Run("tools/call "+id, func(t *testing.T) {
			api, _, _ := fullAddress(t, dataapi.DefaultAnonDailyCap, rangeTierURL)
			rec := postMCP(t, api, rpc(id, "tools/call", jumpsCall), fromIP(connectorX)...)
			requireConnectorToolResult(t, rec, id, connectorBusyText, "tools/call "+id)
			require.Equal(t, 50, retryAfter(t, rec))
		})
		t.Run("tools/list "+id, func(t *testing.T) {
			api, _, _ := fullAddress(t, dataapi.DefaultAnonDailyCap, rangeTierURL)
			rec := postMCP(t, api, rpc(id, "tools/list", ""), fromIP(connectorX)...)
			requireConnectorRPCError(t, rec, id, connectorBusyText, "tools/list "+id)
			require.Equal(t, 50, retryAfter(t, rec))
		})
	}
	api, _, _ := fullAddress(t, dataapi.DefaultAnonDailyCap, rangeTierURL)
	rec := postMCP(t, api, rpc("", "notifications/initialized", ""), fromIP(connectorX)...)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, rec.Body.String())
	// Without a daily cap the route limiter is the only check: the same answer.
	api, _, _ = fullAddress(t, 0, rangeTierURL)
	requireAddressBusy(t, mcpFrom(t, api, connectorX), "no daily cap")
}

// A range caller over its daily cap gets the daily text (no wait in it; Retry-After is the time
// until 00:00 UTC) with the key hint: an isError result for a tools/call, the error -32000 for any
// other method, the id echoed; a notification is 202 with no body.
func TestRangeDailyMCP_RefusalIsJSONRPC(t *testing.T) {
	api, _, _ := spentDaily(t, rangeTierURL)
	for _, id := range []string{`5`, `"d-1"`} {
		rec := postMCP(t, api, rpc(id, "tools/call", jumpsCall), fromIP(connectorX)...)
		requireConnectorToolResult(t, rec, id, connectorDailyText, "tools/call "+id)
		require.Equal(t, 14*3600, retryAfter(t, rec))
		rec = postMCP(t, api, rpc(id, "tools/list", ""), fromIP(connectorX)...)
		requireConnectorRPCError(t, rec, id, connectorDailyText, "tools/list "+id)
		require.Equal(t, 14*3600, retryAfter(t, rec))
	}
	rec := postMCP(t, api, rpc("", "notifications/initialized", ""), fromIP(connectorX)...)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, rec.Body.String())
	// What is not one JSON-RPC message keeps the HTTP 429.
	requireError(t, postMCP(t, api, "["+listToolsRPC+"]", fromIP(connectorX)...), http.StatusTooManyRequests, "daily_quota_exceeded")
	requireError(t, do(api, http.MethodGet, "/v1/mcp", "", fromIP(connectorX)...), http.StatusTooManyRequests, "daily_quota_exceeded")
}

// Without a PublicURL there is no key page to point at: every range-caller text ends at its own
// sentence, with no dangling hint.
func TestRangeMCP_NoPublicURLNoKeyHint(t *testing.T) {
	plain := func(rec *httptest.ResponseRecorder) string {
		reply, _ := requireConnectorReply(t, rec, `1`, "no public URL")
		require.NotNil(t, reply.Error, rec.Body.String())
		return reply.Error.Message
	}
	api, _, _ := fullAddress(t, dataapi.DefaultAnonDailyCap, "")
	require.Equal(t, "This service is busy for Claude connectors right now; try again in 50 seconds.", plain(mcpFrom(t, api, connectorX)))
	api, _, _ = spentDaily(t, "")
	require.Equal(t, "Today's request limit for Claude connectors is used up; it resets at 00:00 UTC.", plain(mcpFrom(t, api, connectorX)))
}

// Only a range caller's refusals on /v1/mcp are JSON-RPC: the REST tool route, a keyless caller
// outside the ranges and a caller with a key keep the HTTP 429 with the REST body.
func TestRangeMCP_OtherCallersAndRoutesKeepTheHTTP429(t *testing.T) {
	api, _, _ := fullAddress(t, dataapi.DefaultAnonDailyCap, rangeTierURL)
	spendOK(t, api, "192.0.2.10", 20)
	requireAddressRefusal(t, postMCP(t, api, rpc("1", "tools/call", jumpsCall), fromIP("192.0.2.10")...), "outside the ranges")
	key := []string{"X-API-Key", keyAlpha}
	for i := range 120 {
		require.Equal(t, http.StatusOK, mcpFrom(t, api, "160.79.104.200", key...).Code, "keyed request %d", i+1)
	}
	requireAddressRefusal(t, postMCP(t, api, rpc("1", "tools/call", jumpsCall), append(fromIP("160.79.104.200"), key...)...), "a key in the ranges")
	for i := range 10 {
		rec := do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{}`, fromIP("160.79.104.201")...)
		require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "REST call %d: %s", i+1, rec.Body.String())
	}
	requireAddressRefusal(t, do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{}`, fromIP("160.79.104.201")...), "REST in the ranges")

	daily, _, _ := spentDaily(t, rangeTierURL)
	requireError(t, do(daily, http.MethodPost, "/v1/tool/get_jumps_between", `{}`, fromIP(connectorX)...), http.StatusTooManyRequests, "daily_quota_exceeded")
	spendOK(t, daily, "192.0.2.10", 2)
	requireError(t, postMCP(t, daily, rpc("1", "tools/call", jumpsCall), fromIP("192.0.2.10")...), http.StatusTooManyRequests, "daily_quota_exceeded")
}

// The spend rules stay: a per-address refusal answered as JSON-RPC spends exactly one daily unit
// (it passed the daily cap before the route limiter refused it), and a daily refusal spends
// nothing more: no place in the address's own window, none in the shared ceiling.
func TestRangeMCP_RefusalsSpendAsBefore(t *testing.T) {
	t.Run("address", func(t *testing.T) {
		const daily = 4
		api, clk, _ := fullAddress(t, daily, rangeTierURL) // 2 of 4 spent
		requireAddressBusy(t, mcpFrom(t, api, connectorX), "the third request: a daily unit spent")
		clk.advance(time.Minute)
		spendOK(t, api, connectorX, 1) // the fourth unit
		requireDailyRefusal(t, mcpFrom(t, api, connectorX), "4 units: 3 served, 1 refused by the address")
	})
	t.Run("daily", func(t *testing.T) {
		clk := newTestClock()
		limits := dataapi.DefaultLimits()
		limits.Range.MCP = dataapi.Rate{Max: 2, Window: time.Minute}
		limits.Range.MCPCeiling = dataapi.Rate{Max: 3, Window: time.Minute}
		limits.Range.DailyCap = 1
		api := newAPI(t, dataapi.Config{
			Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), MCP: okMCP,
			Limits: &limits, AnonDailyCap: 1, Now: clk.now, PublicURL: rangeTierURL,
			AnthropicRanges: mustRanges(t, anthropicOutbound),
		})
		spendOK(t, api, connectorX, 1) // the ceiling holds 1 of 3
		for i := range 20 {
			requireDailyRefusal(t, mcpFrom(t, api, connectorX), fmt.Sprintf("daily refusal %d", i+1))
		}
		spendOK(t, api, "160.79.104.10", 1)
		spendOK(t, api, "153.61.208.11", 1) // 3 of 3: the daily refusals took no place
		requireCeilingRefusal(t, mcpFrom(t, api, "160.79.104.12"), "the ceiling is full only now")
	})
}

// The access line of a JSON-RPC refusal of a range caller's own limits names them,
// range_refused="address" or "daily", and never the address.
func TestRangeMCP_AccessLineNamesTheOwnLimits(t *testing.T) {
	api, _, logs := fullAddress(t, dataapi.DefaultAnonDailyCap, rangeTierURL)
	postMCP(t, api, rpc("1", "tools/call", jumpsCall), fromIP(connectorX)...)
	daily, _, dlogs := spentDaily(t, rangeTierURL)
	postMCP(t, daily, rpc("1", "tools/list", ""), fromIP(connectorX)...)
	postMCP(t, daily, "["+listToolsRPC+"]", fromIP(connectorX)...)
	lines := append(requestLines(t, logs), requestLines(t, dlogs)...)
	require.Len(t, lines, 3)
	require.EqualValues(t, 200, lines[0]["status"])
	require.Equal(t, "address", lines[0]["range_refused"])
	require.EqualValues(t, 200, lines[1]["status"])
	require.Equal(t, "daily", lines[1]["range_refused"])
	require.EqualValues(t, 429, lines[2]["status"])
	require.NotContains(t, lines[2], "range_refused")
	for _, l := range lines {
		require.NotContains(t, fmt.Sprint(l), connectorX, "no address in the access line")
	}
}

// A range caller over its own live-tool budget (15 a minute) on an MCP tools/call gets the
// connector busy text with the key hint in the handler's isError result, and the access line
// says range_refused="address"; the REST call keeps its 429. The shared live ceiling's in-handler
// result has the same text.
func TestRangeLiveMCP_OwnBudgetRefusalIsTheConnectorText(t *testing.T) {
	clk := newTestClock()
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	limits.Range.DailyCap = 0
	deps := &tools.Deps{Client: tools.WithTransport(offlineTransport{})}
	lim := dataapi.NewRangeUpstreamLimiter(limits.UpstreamTool, limits.Range, nil, clk.now)
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t),
		Limits: &limits, Now: clk.now, PublicURL: rangeTierURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound), Upstream: lim, Logger: log,
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true, Keyed: dataapi.KeyedTier, Upstream: dataapi.UpstreamHook(lim),
		}),
	})
	addrs := rangeAddrs(3)
	for i := range 15 {
		requireLiveServed(t, liveCall(t, api, addrs[0], i), addrs[0])
	}
	buf.Reset()
	res := rpcResult(t, liveMCP(t, api, addrs[0]))
	require.Equal(t, true, res["isError"], fmt.Sprint(res))
	require.Equal(t, connectorBusyText(60), res["content"].([]any)[0].(map[string]any)["text"])
	requireOwnLiveRefusal(t, liveREST(api, addrs[0]), "REST keeps the 429")
	lines := requestLines(t, buf)
	require.Len(t, lines, 2)
	require.Equal(t, "address", lines[0]["range_refused"])
	require.NotContains(t, lines[1], "range_refused")

	for i := range 15 {
		requireLiveServed(t, liveCall(t, api, addrs[1], i), addrs[1])
	}
	buf.Reset()
	res = rpcResult(t, liveMCP(t, api, addrs[2]))
	require.Equal(t, true, res["isError"], fmt.Sprint(res))
	require.Equal(t, connectorBusyText(60), res["content"].([]any)[0].(map[string]any)["text"], "the shared ceiling in the handler")
	require.Equal(t, "live_ceiling", requestLines(t, buf)[0]["range_refused"])
	for _, l := range lines {
		require.NotContains(t, fmt.Sprint(l), addrs[0], "no address in the access line")
	}
}

// With both pools of /v1/mcp full a request's body is never read, not even to answer a range
// caller in JSON-RPC: the refusal the chain would give falls back to its HTTP 429.
func TestRangeMCP_BothPoolsFullReadsNoBody(t *testing.T) {
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.MCPCeiling = dataapi.Rate{Max: 2, Window: time.Minute}
	mcp := newHoldMCP()
	api := newAPI(t, dataapi.Config{
		MCP: mcp, Auth: testKeys(t), Limits: &limits, AnonDailyCap: dataapi.DefaultAnonDailyCap, Now: clk.now,
		MaxInFlight: 1, MaxInFlightUpstream: 1, PublicURL: rangeTierURL, AnthropicRanges: mustRanges(t, anthropicOutbound),
	})
	var done []<-chan int
	for _, c := range []struct{ ip, body string }{{"160.79.104.10", mcpCall}, {"153.61.208.11", sovCall}} {
		done = append(done, start(api, http.MethodPost, "/v1/mcp", c.body, append(fromIP(c.ip), holdHeader, "1")...))
		mcp.waitEntered(t)
	}
	// Both pools are full, and so is the ceiling of 2.
	var reads atomic.Int32
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", countingBody{strings.NewReader(mcpCall), &reads})
	req.Header.Set("CF-Connecting-IP", connectorX)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	requireRESTCeilingRefusal(t, rec, 2, "both pools full")
	require.Zero(t, reads.Load(), "no body is read while both pools are full")
	close(mcp.release)
	for _, d := range done {
		require.Equal(t, http.StatusOK, <-d)
	}
}
