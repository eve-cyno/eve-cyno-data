package dataapi_test

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

// checkingQuota is a Quota that also implements dataapi.QuotaChecker: left is the budget per
// key id; it counts every Spend and every Check call.
type checkingQuota struct {
	mu     sync.Mutex
	left   map[string]int
	spends int
	checks int
}

func (q *checkingQuota) Spend(id string) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.spends++
	if q.left[id] <= 0 {
		return false, 90 * time.Minute
	}
	q.left[id]--
	return true, 0
}

func (q *checkingQuota) Check(id string) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.checks++
	if q.left[id] <= 0 {
		return false, 90 * time.Minute
	}
	return true, 0
}

func (q *checkingQuota) counts() (spends, checks int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.spends, q.checks
}

var _ dataapi.QuotaChecker = (*checkingQuota)(nil)

// retryAfter is the Retry-After header of a response in whole seconds.
func retryAfter(t *testing.T, rec interface{ Header() http.Header }) int {
	t.Helper()
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	return secs
}

// While a route is full, a caller that the real chain would refuse gets that 429 (its code,
// body and Retry-After), in the chain's order: the key's quota, the anonymous daily cap, the
// route's per-minute bucket. A caller under every limit gets 503 busy.
func TestInflight_BusyAnswersTheRealRetryAfterToAnOverLimitCaller(t *testing.T) {
	const key = "ecd_test_key_one"
	minute := &dataapi.Limits{MCP: dataapi.Rate{Max: 1, Window: time.Minute}}

	t.Run("per-minute limit", func(t *testing.T) {
		log, buf := logSink()
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Limits: minute, MaxInFlight: 1, LogSalt: "test-salt", Logger: log})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)

		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, append(fromIP("203.0.113.9"), dataapi.HeaderRequestID, "over-minute")...)
		requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
		secs := retryAfter(t, rec)
		require.Greater(t, secs, 1, "the bucket's own wait, not the busy answer's 1 s")
		require.LessOrEqual(t, secs, 60)
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.10")...), http.StatusServiceUnavailable, "busy")
		require.Equal(t, http.StatusOK, release())

		l := requestLine(t, logLines(t, buf), "over-minute")
		require.EqualValues(t, http.StatusTooManyRequests, l["status"])
		require.Equal(t, "/v1/mcp", l["route"])
		require.Regexp(t, `^[0-9a-f]{16}$`, l["caller"])
		require.NotContains(t, l, "busy_pool", "the answer is the real 429, not a busy refusal")
	})

	t.Run("anonymous daily cap before the per-minute limit", func(t *testing.T) {
		c := &clock{t: time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{
			MCP: mcp, Auth: keyedFixture(t, key), Limits: minute, AnonDailyCap: 1, Now: c.now, MaxInFlight: 1,
			PublicURL: "https://data.example",
		})
		// One request fills both the daily cap and the per-minute bucket: the chain answers
		// the daily cap first.
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		defer release()

		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...)
		requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
		require.Equal(t, 7200, retryAfter(t, rec), "the seconds to 00:00 UTC")
		require.Contains(t, rec.Body.String(), "without an API key the API allows 1 requests a day per client; a free key raises this: https://data.example/docs/access#access-keys")
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.10")...), http.StatusServiceUnavailable, "busy")
	})

	t.Run("key quota before the per-minute limit", func(t *testing.T) {
		q := &checkingQuota{left: map[string]int{"k1": 1}}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, key), Quota: q, Limits: minute, MaxInFlight: 1})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		defer release()

		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key)
		requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
		require.Equal(t, 5400, retryAfter(t, rec))
		require.Contains(t, rec.Body.String(), "this API key has used its daily request quota")
	})

	t.Run("a quota that cannot check stays busy", func(t *testing.T) {
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, key), Quota: &countingQuota{}, Limits: &dataapi.Limits{}, MaxInFlight: 1})
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		defer release()
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key), http.StatusServiceUnavailable, "busy")
	})
}

// The checks a busy route runs record nothing: after 20 refusals the route limiter, the
// anonymous daily cap and the key's quota still hold the budget they held before.
func TestInflight_BusyChecksRecordNothing(t *testing.T) {
	const key = "ecd_test_key_one"
	t.Run("route limiter", func(t *testing.T) {
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Limits: &dataapi.Limits{MCP: dataapi.Rate{Max: 3, Window: time.Minute}}, MaxInFlight: 1})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		for range 20 {
			requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusServiceUnavailable, "busy")
		}
		require.Equal(t, http.StatusOK, release())
		for i := range 2 {
			require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code, "request %d", i+2)
		}
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusTooManyRequests, "rate_limited")
	})
	t.Run("anonymous daily cap", func(t *testing.T) {
		c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, key), Limits: &dataapi.Limits{}, AnonDailyCap: 3, Now: c.now, MaxInFlight: 1})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		for range 20 {
			requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusServiceUnavailable, "busy")
		}
		require.Equal(t, http.StatusOK, release())
		for i := range 2 {
			require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code, "request %d", i+2)
		}
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusTooManyRequests, "daily_quota_exceeded")
	})
	t.Run("key quota", func(t *testing.T) {
		q := &checkingQuota{left: map[string]int{"k1": 3}}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, key), Quota: q, Limits: &dataapi.Limits{}, MaxInFlight: 1})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key).Code)
		release := hold(t, api, mcp, fromIP("198.51.100.200")...)
		for range 20 {
			requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key), http.StatusServiceUnavailable, "busy")
		}
		require.Equal(t, http.StatusOK, release())
		spends, checks := q.counts()
		require.Equal(t, 1, spends, "a busy refusal never spends")
		require.Equal(t, 20, checks, "every busy refusal checked the quota")
		for i := range 2 {
			require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key).Code, "request %d", i+2)
		}
		requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, "X-API-Key", key), http.StatusTooManyRequests, "daily_quota_exceeded")
	})
}

// A 429 answered at a full route is the real chain's 429 in the books too: the same usage
// record and the same access line (route, tool, caller, no busy_pool).
func TestInflight_OverLimitAnswerAtAFullRouteIsRecordedLikeTheRealOne(t *testing.T) {
	const key = "ecd_test_key_one"
	body := `{"from_system":"Jita","to_system":"Amarr"}`
	log, buf := logSink()
	u := &fakeUsage{}
	api := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Auth: keyedFixture(t, key), Usage: u,
		Limits: &dataapi.Limits{Tool: dataapi.Rate{Max: 1, Window: time.Minute}}, MaxInFlight: 1,
		LogSalt: "test-salt", Logger: log,
	})
	do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, "X-API-Key", key)
	chain := do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, "X-API-Key", key, dataapi.HeaderRequestID, "chain")
	requireError(t, chain, http.StatusTooManyRequests, "rate_limited")

	release := holdWriting(t, api, http.MethodPost, "/v1/tool/get_jumps_between", body, fromIP("198.51.100.200")...)
	gate := do(api, http.MethodPost, "/v1/tool/get_jumps_between", body, "X-API-Key", key, dataapi.HeaderRequestID, "gate")
	release()
	requireError(t, gate, http.StatusTooManyRequests, "rate_limited")
	require.Greater(t, retryAfter(t, gate), 1)
	require.Equal(t, "no-store", chain.Header().Get("Cache-Control"))
	require.Equal(t, chain.Header().Get("Cache-Control"), gate.Header().Get("Cache-Control"), "the route's cache policy applies to the gate's 429 too")

	recs := u.all()
	require.Len(t, recs, 3)
	require.Equal(t, usageRecord{"k1", "tool:get_jumps_between", true}, recs[1])
	require.Equal(t, recs[1], recs[2], "the gate's 429 is recorded like the chain's")

	lines := logLines(t, buf)
	want := requestLine(t, lines, "chain")
	got := requestLine(t, lines, "gate")
	for _, attr := range []string{"route", "tool", "caller", "status"} {
		require.Equal(t, want[attr], got[attr], attr)
	}
	require.NotContains(t, got, "busy_pool")
}

// While the route is full, a keyless caller inside the ranges whose shared ceiling is full gets
// the ceiling's 429 with its wait, as the chain would answer: with a daily cap the ceiling is
// checked before the cap (so a spent cap still answers the ceiling, not daily_quota_exceeded),
// and without one the route's range pair answers it.
func TestInflight_BusyAnswersTheRangeCeilingToARangeCaller(t *testing.T) {
	const x, y, outside = "160.79.104.9", "153.61.208.20", "192.0.2.200"
	rangeAPI := func(t *testing.T, anonCap int) (*dataapi.API, *holdMCP, *clock) {
		t.Helper()
		c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
		limits := dataapi.DefaultLimits()
		limits.Range.MCPCeiling = dataapi.Rate{Max: 2, Window: time.Minute}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{
			MCP: mcp, Auth: keyedFixture(t, "ecd_test_key_one"), Limits: &limits, AnonDailyCap: anonCap,
			Now: c.now, MaxInFlight: 1, PublicURL: rangeTierURL, AnthropicRanges: mustRanges(t, anthropicOutbound),
		})
		// x and y fill the ceiling of 2; x has 59 of its own 60 left.
		for _, ip := range []string{x, y} {
			require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(ip)...).Code, ip)
		}
		c.t = c.t.Add(10 * time.Second)
		return api, mcp, c
	}

	t.Run("daily cap spent", func(t *testing.T) {
		api, mcp, _ := rangeAPI(t, 1) // x's one request spent its whole daily cap
		release := hold(t, api, mcp, fromIP(outside)...)
		defer release()
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(x)...)
		requireCeilingToolResult(t, rec, `1`, "the ceiling is checked before the daily cap")
		require.Equal(t, 50, retryAfter(t, rec))
	})
	t.Run("no daily cap", func(t *testing.T) {
		api, mcp, _ := rangeAPI(t, 0)
		release := hold(t, api, mcp, fromIP(outside)...)
		defer release()
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(x)...)
		requireCeilingToolResult(t, rec, `1`, "the route's range pair")
		require.Equal(t, 50, retryAfter(t, rec))
	})
}

// While the route is full (one pool, so the body may be read), a keyless caller inside the ranges
// that its own address limit or its daily cap refuses gets that refusal in JSON-RPC on /v1/mcp,
// as the chain would answer it, not 503 busy and not the HTTP 429.
func TestInflight_BusyAnswersARangeCallersOwnLimitsInJSONRPC(t *testing.T) {
	const x, outside = "160.79.104.9", "192.0.2.200"
	rangeAPI := func(t *testing.T, limits dataapi.Limits) (*dataapi.API, *holdMCP) {
		t.Helper()
		c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
		mcp := newHoldMCP()
		api := newAPI(t, dataapi.Config{
			MCP: mcp, Auth: keyedFixture(t, "ecd_test_key_one"), Limits: &limits, AnonDailyCap: dataapi.DefaultAnonDailyCap,
			Now: c.now, MaxInFlight: 1, PublicURL: rangeTierURL, AnthropicRanges: mustRanges(t, anthropicOutbound),
		})
		require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(x)...).Code, "x's one request")
		c.t = c.t.Add(10 * time.Second)
		return api, mcp
	}

	t.Run("address", func(t *testing.T) {
		limits := dataapi.DefaultLimits()
		limits.Range.MCP = dataapi.Rate{Max: 1, Window: time.Minute}
		api, mcp := rangeAPI(t, limits)
		release := hold(t, api, mcp, fromIP(outside)...)
		defer release()
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(x)...)
		requireConnectorToolResult(t, rec, `1`, connectorBusyText, "the address limit at a full route")
		require.Equal(t, 50, retryAfter(t, rec))
	})
	t.Run("daily cap", func(t *testing.T) {
		limits := dataapi.DefaultLimits()
		limits.Range.DailyCap = 1
		api, mcp := rangeAPI(t, limits)
		release := hold(t, api, mcp, fromIP(outside)...)
		defer release()
		rec := do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(x)...)
		requireConnectorToolResult(t, rec, `1`, connectorDailyText, "the daily cap at a full route")
	})
}

// A caller that filled yesterday's anonymous daily cap is not over the cap after 00:00 UTC:
// at a full route it gets 503 busy, not the stale daily-cap 429, and the next request that
// the route serves starts the new day and succeeds.
func TestInflight_BusyAfterMidnightIgnoresYesterdaysAnonCap(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC)}
	mcp := newHoldMCP()
	api := newAPI(t, dataapi.Config{MCP: mcp, Auth: keyedFixture(t, "ecd_test_key_one"), Limits: &dataapi.Limits{}, AnonDailyCap: 1, Now: c.now, MaxInFlight: 1})
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusTooManyRequests, "daily_quota_exceeded")

	// The route fills before the clock passes midnight, so no Spend has started the new day
	// when the caller arrives: only Check's own day comparison can tell it is under the cap.
	release := hold(t, api, mcp, fromIP("198.51.100.200")...)
	c.t = time.Date(2026, 11, 12, 0, 0, 1, 0, time.UTC)
	requireError(t, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...), http.StatusServiceUnavailable, "busy")
	require.Equal(t, http.StatusOK, release())
	require.Equal(t, http.StatusOK, do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP("203.0.113.9")...).Code)
}
