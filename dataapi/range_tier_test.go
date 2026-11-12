package dataapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// rangeTierURL is the public origin of the range-tier fixtures; the ceiling message points at
// its key page.
const rangeTierURL = "https://data.example.org"

// ceilingMessage is the 429 message of the shared range ceiling of max requests per minute, which
// every answer that is not a JSON-RPC message on /v1/mcp keeps (REST, a batch, a body that does
// not parse, GET).
func ceilingMessage(max int) string {
	return fmt.Sprintf("the shared limit of %d requests per minute for all keyless claude.ai and Claude Desktop connector calls is reached; "+
		"retry after Retry-After seconds, or use a free API key for a budget of your own: %s/docs/access#access-keys", max, rangeTierURL)
}

// connectorKeyHint is the clause every refusal text of a range caller ends with on the
// range-tier fixtures (PublicURL rangeTierURL): the key page, for a client that can send a key.
const connectorKeyHint = " Clients that can send an Authorization header can use a free API key: " + rangeTierURL + "/docs/access#access-keys"

// connectorBusyText is the one text of a range caller's per-minute refusal (the shared ceiling or
// its own address limit) of a JSON-RPC message on /v1/mcp, with the wait n in whole seconds.
func connectorBusyText(n int) string {
	return fmt.Sprintf("This service is busy for Claude connectors right now; try again in %d seconds.", n) + connectorKeyHint
}

// connectorDailyText is the text of a range caller's daily cap refusal on /v1/mcp (no wait in it;
// Retry-After carries the seconds until 00:00 UTC).
func connectorDailyText(int) string {
	return "Today's request limit for Claude connectors is used up; it resets at 00:00 UTC." + connectorKeyHint
}

// rpcReply is the strict shape of a JSON-RPC answer to a refused MCP message.
type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// requireConnectorReply asserts the parts every JSON-RPC answer of a range caller's refusal
// shares: HTTP 200, application/json, Retry-After in whole seconds (at least 1), the id echoed as
// wantID (raw JSON). It returns the reply and the wait.
func requireConnectorReply(t *testing.T, rec *httptest.ResponseRecorder, wantID, msg string) (rpcReply, int) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "%s: %s", msg, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"), msg)
	secs := retryAfter(t, rec)
	require.GreaterOrEqual(t, secs, 1, msg)
	var reply rpcReply
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&reply), "%s: %s", msg, rec.Body.String())
	require.Equal(t, "2.0", reply.JSONRPC, msg)
	require.JSONEq(t, wantID, string(reply.ID), "%s: the request id is echoed", msg)
	return reply, secs
}

// requireConnectorRPCError asserts the JSON-RPC error -32000 of a range caller's refusal of a
// request with the id wantID (raw JSON) that is not a tools/call, with the text text(wait).
func requireConnectorRPCError(t *testing.T, rec *httptest.ResponseRecorder, wantID string, text func(int) string, msg string) {
	t.Helper()
	reply, secs := requireConnectorReply(t, rec, wantID, msg)
	require.Nil(t, reply.Result, msg)
	require.NotNil(t, reply.Error, msg)
	require.Equal(t, -32000, reply.Error.Code, msg)
	require.Equal(t, text(secs), reply.Error.Message, msg)
}

// requireConnectorToolResult asserts a range caller's refusal of a tools/call with the id wantID
// (raw JSON): a result with isError and text(wait) as its one content item.
func requireConnectorToolResult(t *testing.T, rec *httptest.ResponseRecorder, wantID string, text func(int) string, msg string) {
	t.Helper()
	reply, secs := requireConnectorReply(t, rec, wantID, msg)
	require.Nil(t, reply.Error, msg)
	require.NotNil(t, reply.Result, msg)
	require.True(t, reply.Result.IsError, msg)
	require.Len(t, reply.Result.Content, 1, msg)
	require.Equal(t, "text", reply.Result.Content[0].Type, msg)
	require.Equal(t, text(secs), reply.Result.Content[0].Text, msg)
}

// requireCeilingRefusal asserts the range ceiling's answer to an MCP request that is not a
// tools/call (here tools/list with id 1): a JSON-RPC error -32000 with the busy text.
func requireCeilingRefusal(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	requireCeilingRPCError(t, rec, `1`, msg)
}

// requireAddressBusy asserts a range caller's own address limit refusing tools/list with id 1 on
// /v1/mcp: the same JSON-RPC error and text as the ceiling's (only the access line tells them
// apart).
func requireAddressBusy(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	requireCeilingRPCError(t, rec, `1`, msg)
}

// requireDailyRefusal asserts a range caller's daily cap refusing tools/list with id 1 on
// /v1/mcp: the JSON-RPC error -32000 with the daily text.
func requireDailyRefusal(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	requireConnectorRPCError(t, rec, `1`, connectorDailyText, msg)
}

// requireCeilingRPCError asserts the JSON-RPC error answer of the range ceiling to a request
// with the id wantID (raw JSON).
func requireCeilingRPCError(t *testing.T, rec *httptest.ResponseRecorder, wantID, msg string) {
	t.Helper()
	requireConnectorRPCError(t, rec, wantID, connectorBusyText, msg)
}

// requireCeilingToolResult asserts the range ceiling's answer to a tools/call with the id wantID
// (raw JSON): a result with isError and the busy text as its one content item.
func requireCeilingToolResult(t *testing.T, rec *httptest.ResponseRecorder, wantID, msg string) {
	t.Helper()
	requireConnectorToolResult(t, rec, wantID, connectorBusyText, msg)
}

// requireRESTCeilingRefusal asserts the HTTP 429 of the shared range ceiling of max per minute,
// the answer outside JSON-RPC.
func requireRESTCeilingRefusal(t *testing.T, rec *httptest.ResponseRecorder, max int, msg string) {
	t.Helper()
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	require.Equal(t, ceilingMessage(max), errorMessage(t, rec), msg)
	require.NotEmpty(t, rec.Header().Get("Retry-After"), msg)
}

// testClock is a settable clock for Config.Now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)} }

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// okMCP stands in for the MCP SDK: every request that reaches it is a 200.
var okMCP = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
})

// rangeTierAPI serves MCP (okMCP) with API keys, the given limits (nil: DefaultLimits), the
// anonymous daily cap and the Anthropic ranges ("" none) on the clock.
func rangeTierAPI(t *testing.T, ranges string, limits *dataapi.Limits, anonCap int, clk *testClock) *dataapi.API {
	t.Helper()
	if limits == nil {
		l := dataapi.DefaultLimits()
		limits = &l
	}
	if clk == nil {
		clk = newTestClock()
	}
	return newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t), MCP: okMCP,
		Limits: limits, AnonDailyCap: anonCap, Now: clk.now, PublicURL: rangeTierURL,
		AnthropicRanges: mustRanges(t, ranges),
	})
}

// mcpFrom sends one MCP request from the client address ip (CF-Connecting-IP).
func mcpFrom(t *testing.T, api http.Handler, ip string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	return postMCP(t, api, listToolsRPC, append(fromIP(ip), hdr...)...)
}

// errorMessage returns the message of a JSON error body.
func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Message
}

// requireAddressRefusal asserts the plain per-caller 429.
func requireAddressRefusal(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	require.Equal(t, "too many requests, slow down", errorMessage(t, rec), msg)
	require.NotEmpty(t, rec.Header().Get("Retry-After"), msg)
}

// rangeAddrs are n distinct client addresses inside Anthropic's outbound ranges, alternating
// between the /21 and the /20.
func rangeAddrs(n int) []string {
	out := make([]string, n)
	for i := range n {
		if i%2 == 0 {
			out[i] = fmt.Sprintf("160.79.104.%d", 10+i)
		} else {
			out[i] = fmt.Sprintf("153.61.208.%d", 10+i)
		}
	}
	return out
}

// spendOK makes n MCP requests from ip and requires each to be served.
func spendOK(t *testing.T, api http.Handler, ip string, n int) {
	t.Helper()
	for i := range n {
		rec := mcpFrom(t, api, ip)
		require.Equal(t, http.StatusOK, rec.Code, "request %d from %s: %s", i+1, ip, rec.Body.String())
	}
}

// A keyless caller inside the ranges gets 60 MCP requests a minute (not the 20 of other
// keyless callers); the 61st is refused by its own address limit, not by the ceiling.
func TestRangeTier_PerAddressSixtyPerMinute(t *testing.T) {
	clk := newTestClock()
	api := rangeTierAPI(t, anthropicOutbound, nil, dataapi.DefaultAnonDailyCap, clk)
	require.Equal(t, 60, dataapi.DefaultLimits().Range.MCP.Max)

	spendOK(t, api, "160.79.104.9", 60)
	requireAddressBusy(t, mcpFrom(t, api, "160.79.104.9"), "the 61st request of one address")

	// Another address of the range has its own 60.
	spendOK(t, api, "153.61.215.1", 60)

	clk.advance(time.Minute + time.Second)
	spendOK(t, api, "160.79.104.9", 1)
}

// The ceiling is one bucket for every address of the ranges: six addresses of 60 each get 300
// requests together, and the 301st is refused with the ceiling message.
func TestRangeTier_CeilingIsSharedAcrossAddresses(t *testing.T) {
	require.Equal(t, 300, dataapi.DefaultLimits().Range.MCPCeiling.Max)
	// With a daily cap the full ceiling is answered before the cap (ServeHTTP); without one the
	// route limiter answers it. The answer is the same.
	for _, anonCap := range []int{dataapi.DefaultAnonDailyCap, 0} {
		t.Run(fmt.Sprintf("daily cap %d", anonCap), func(t *testing.T) {
			api := rangeTierAPI(t, anthropicOutbound, nil, anonCap, nil)
			n := 0
			for _, ip := range rangeAddrs(6) {
				for range 60 {
					n++
					rec := mcpFrom(t, api, ip)
					if n <= 300 {
						require.Equal(t, http.StatusOK, rec.Code, "request %d: %s", n, rec.Body.String())
						continue
					}
					requireCeilingRefusal(t, rec, fmt.Sprintf("request %d", n))
				}
			}
			require.Equal(t, 360, n)
		})
	}
}

// A request the ceiling refuses spends nothing of its address's own 60.
func TestRangeTier_CeilingRefusalSpendsNoAddressBudget(t *testing.T) {
	clk := newTestClock()
	api := rangeTierAPI(t, anthropicOutbound, nil, dataapi.DefaultAnonDailyCap, clk)
	addrs := rangeAddrs(6)
	for _, ip := range addrs[:5] {
		spendOK(t, api, ip, 60)
	}
	x := addrs[5]
	for i := range 10 {
		requireCeilingRefusal(t, mcpFrom(t, api, x), fmt.Sprintf("refusal %d", i+1))
	}

	clk.advance(time.Minute + time.Second) // the ceiling empties
	spendOK(t, api, x, 60)
	requireAddressBusy(t, mcpFrom(t, api, x), "the address still had its whole 60")
}

// A request its own address limit refuses never takes a place in the shared ceiling.
func TestRangeTier_AddressRefusalDoesNotDrainTheCeiling(t *testing.T) {
	api := rangeTierAPI(t, anthropicOutbound, nil, dataapi.DefaultAnonDailyCap, nil)
	addrs := rangeAddrs(6)
	spendOK(t, api, addrs[0], 60)
	for i := range 100 {
		requireAddressBusy(t, mcpFrom(t, api, addrs[0]), fmt.Sprintf("refusal %d", i+1))
	}
	for _, ip := range addrs[1:5] {
		spendOK(t, api, ip, 60)
	}
	// 300 served: the ceiling is full now, and only now.
	requireCeilingRefusal(t, mcpFrom(t, api, addrs[5]), "the 301st served request")
}

// With the ceiling full from other addresses, the ceiling refusals of an address spend none of
// its anonymous daily cap: it is refused before the cap is spent.
func TestRangeTier_CeilingRefusalSpendsNoDailyCap(t *testing.T) {
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.MCPCeiling = dataapi.Rate{Max: 25, Window: time.Minute}
	const daily = 20 // below the ceiling: the address alone can never fill it
	limits.Range.DailyCap = daily
	api := rangeTierAPI(t, anthropicOutbound, &limits, daily, clk)

	for _, ip := range rangeAddrs(25) {
		spendOK(t, api, ip, 1)
	}
	const x = "160.79.111.200"
	for i := range 50 {
		requireCeilingRefusal(t, mcpFrom(t, api, x), fmt.Sprintf("refusal %d", i+1))
	}

	clk.advance(time.Minute + time.Second) // the ceiling empties, the UTC day does not change
	spendOK(t, api, x, daily)
	requireDailyRefusal(t, mcpFrom(t, api, x), "the daily cap")
}

// With its own limit and the ceiling both full, an address whose own wait is the longer one gets
// its own refusal and wait, not the ceiling's shorter one: Retry-After must not send it back
// before it can be served. Its address is then the binding limit, so the refusal spends the
// daily cap like every per-address refusal (on /v1/mcp answered in JSON-RPC, the same text as the
// ceiling's with the address's wait). The same answer with and without a daily cap (the
// ceiling check before the cap, the route limiter).
func TestRangeTier_BothFullAnswersTheAddressWaitWhenLonger(t *testing.T) {
	for _, anonCap := range []int{dataapi.DefaultAnonDailyCap, 0} {
		t.Run(fmt.Sprintf("daily cap %d", anonCap), func(t *testing.T) {
			clk := newTestClock()
			limits := dataapi.DefaultLimits()
			limits.Range.MCP = dataapi.Rate{Max: 60, Window: time.Minute}
			limits.Range.MCPCeiling = dataapi.Rate{Max: 61, Window: time.Minute}
			api := rangeTierAPI(t, anthropicOutbound, &limits, anonCap, clk)
			const x = "160.79.104.9"

			clk.advance(time.Second)
			spendOK(t, api, "153.61.208.20", 1) // t=1s: the ceiling's oldest
			clk.advance(29 * time.Second)
			spendOK(t, api, x, 60) // t=30s: x's oldest; x and the ceiling are full
			clk.advance(25 * time.Second)

			// t=55s: the ceiling frees in 6 s, x's own limit in 35 s.
			rec := mcpFrom(t, api, x)
			requireAddressBusy(t, rec, "x's own wait is the longer one")
			require.Equal(t, 35, retryAfter(t, rec))
			// Another address of the ranges is held by the ceiling alone: its wait.
			rec = mcpFrom(t, api, "153.61.208.30")
			requireCeilingRefusal(t, rec, "a fresh address")
			require.Equal(t, 6, retryAfter(t, rec))
		})
	}
}

// With both full and the ceiling's wait as long as the address's own (the address's oldest
// request is the ceiling's oldest), the refusal is the ceiling's, and it spends no daily unit.
func TestRangeTier_BothFullAnswersTheCeilingWhenItsWaitIsAsLong(t *testing.T) {
	clk := newTestClock()
	limits := dataapi.DefaultLimits()
	limits.Range.MCP = dataapi.Rate{Max: 2, Window: time.Minute}
	limits.Range.MCPCeiling = dataapi.Rate{Max: 2, Window: time.Minute}
	const daily = 3
	limits.Range.DailyCap = daily
	api := rangeTierAPI(t, anthropicOutbound, &limits, daily, clk)
	const x = "160.79.104.9"

	spendOK(t, api, x, 2)
	clk.advance(10 * time.Second)
	for i := range 5 {
		rec := mcpFrom(t, api, x)
		requireCeilingRefusal(t, rec, fmt.Sprintf("refusal %d", i+1))
		require.Equal(t, 50, retryAfter(t, rec))
	}

	clk.advance(51 * time.Second)
	spendOK(t, api, x, 1) // the daily cap's third unit: the five refusals spent none
	requireDailyRefusal(t, mcpFrom(t, api, x), "the daily cap")
}

// Without API keys there is no range tier: an address of the ranges is served like every caller
// of such a service, with the keyed number as everyone's budget (120 a minute), not the range
// tier's 60 per address.
func TestRangeTier_NoKeysNoTier(t *testing.T) {
	limits := dataapi.DefaultLimits().WithoutKeys()
	require.Equal(t, 120, limits.MCP.Max)
	api := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, MCP: okMCP, Limits: &limits,
		Now: newTestClock().now, PublicURL: rangeTierURL, AnthropicRanges: mustRanges(t, anthropicOutbound),
	})
	spendOK(t, api, "160.79.104.9", 120)
	requireAddressRefusal(t, mcpFrom(t, api, "160.79.104.9"), "the 121st request")
}

// A caller with a valid key inside the ranges keeps its key's buckets (KeyedMax 120) and is not
// counted in the shared ceiling.
func TestRangeTier_KeyedCallerInRangeKeepsItsKeyBuckets(t *testing.T) {
	api := rangeTierAPI(t, anthropicOutbound, nil, dataapi.DefaultAnonDailyCap, nil)
	const ip = "160.79.104.9"
	auth := []string{"Authorization", "Bearer " + keyAlpha}
	for i := range 120 {
		rec := mcpFrom(t, api, ip, auth...)
		require.Equal(t, http.StatusOK, rec.Code, "keyed request %d: %s", i+1, rec.Body.String())
	}
	requireAddressRefusal(t, mcpFrom(t, api, ip, auth...), "the key's 121st request")

	// The 120 keyed requests took nothing of the ceiling: keyless range callers still get 300.
	for _, ip := range rangeAddrs(5) {
		spendOK(t, api, ip, 60)
	}
	requireCeilingRefusal(t, mcpFrom(t, api, rangeAddrs(6)[5]), "the 301st keyless request")
}

// Callers outside the ranges keep today's 20 a minute, are never refused by the ceiling, and
// never take a place in it.
func TestRangeTier_OutsideTheRangeIsUnchanged(t *testing.T) {
	api := rangeTierAPI(t, anthropicOutbound, nil, dataapi.DefaultAnonDailyCap, nil)
	outside := []string{"192.0.2.10", "192.0.2.11", "192.0.2.12", "2001:db8::1", "2001:db8:0:1::1"}
	for _, ip := range outside {
		spendOK(t, api, ip, 20)
		requireAddressRefusal(t, mcpFrom(t, api, ip), "the 21st request of "+ip)
	}
	// The outside requests took nothing of the ceiling.
	for _, ip := range rangeAddrs(5) {
		spendOK(t, api, ip, 60)
	}
	requireCeilingRefusal(t, mcpFrom(t, api, rangeAddrs(6)[5]), "the ceiling is full")
	// A full ceiling refuses nobody outside the ranges.
	spendOK(t, api, "192.0.2.50", 20)
	requireAddressRefusal(t, mcpFrom(t, api, "192.0.2.50"), "its own limit, not the ceiling")
}

// Without ranges there is no tier: an address of the Anthropic ranges is an ordinary keyless
// caller with 20 a minute.
func TestRangeTier_EmptyRangesIsUnchanged(t *testing.T) {
	api := rangeTierAPI(t, "", nil, dataapi.DefaultAnonDailyCap, nil)
	spendOK(t, api, "160.79.104.9", 20)
	requireAddressRefusal(t, mcpFrom(t, api, "160.79.104.9"), "the 21st request")
	for _, ip := range rangeAddrs(20) {
		spendOK(t, api, ip, 20) // 400 in all: no ceiling
	}
}

// The R3.9 baseline tools are public work-class tools on /v1/mcp: a keyless range caller's
// tools/call of either one is counted in the same per-address and shared MCP buckets as any
// other request.
func TestRangeTier_ActivityBaselineToolsShareTheMCPBucket(t *testing.T) {
	baseline := []string{"get_activity_baseline", "compare_activity_to_baseline"}
	for _, name := range baseline {
		tl, ok := catalog.Lookup(name)
		require.True(t, ok, name)
		require.Equal(t, catalog.TierPublic, tl.Tier, name)
		require.False(t, tl.Upstream, "%s is work class, not a live-upstream tool", name)
	}
	deps := &tools.Deps{SDE: realSDE(t)}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), ToolAPI: dataapi.ToolAPIPublic, Auth: testKeys(t),
		AnonDailyCap: dataapi.DefaultAnonDailyCap, PublicURL: rangeTierURL,
		AnthropicRanges: mustRanges(t, anthropicOutbound),
		// A clock that stands still: the 360 calls stay inside one window however slow the run.
		Now: newTestClock().now,
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true,
			Keyed:                      func(r *http.Request) bool { return dataapi.CallerFrom(r.Context()).Allows(catalog.TierKeyed) },
		}),
	})
	args := []map[string]any{{"system_name": "Jita"}, {"systems": []string{"Jita"}}}
	call := func(ip string, i int) *httptest.ResponseRecorder {
		return callTool(t, api, baseline[i%2], args[i%2], fromIP(ip)...)
	}
	addrs := rangeAddrs(6)
	for i := range 60 {
		rec := call(addrs[0], i)
		require.Equal(t, http.StatusOK, rec.Code, "call %d: %s", i+1, rec.Body.String())
		rpcResult(t, rec)
	}
	requireConnectorToolResult(t, call(addrs[0], 60), `7`, connectorBusyText, "the 61st baseline call of one address")
	requireAddressBusy(t, mcpFrom(t, api, addrs[0]), "any MCP request after it: one bucket")
	for _, ip := range addrs[1:5] {
		for i := range 60 {
			require.Equal(t, http.StatusOK, call(ip, i).Code, "%s call %d", ip, i+1)
		}
	}
	requireCeilingToolResult(t, call(addrs[5], 0), `7`, "the 301st baseline call of the ranges")
	requireCeilingRefusal(t, mcpFrom(t, api, addrs[5]), "a tools/list shares the ceiling")
}
