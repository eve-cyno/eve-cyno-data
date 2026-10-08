package dataapi_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/tools"
)

// limitsOf returns the default budgets with the given overrides applied.
func limitsOf(mut func(*dataapi.Limits)) *dataapi.Limits {
	l := dataapi.DefaultLimits()
	mut(&l)
	return &l
}

func TestDefaultLimits_MatchTheBudgetsNativeHad(t *testing.T) {
	l := dataapi.DefaultLimits()
	require.Equal(t, dataapi.Rate{Max: 120, Window: time.Minute}, l.FitsSearch)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitsDetail)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitStats)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitSuggest)
	require.Equal(t, dataapi.Rate{Max: 120, Window: time.Minute}, l.ItemsSearch)
	require.Equal(t, dataapi.Rate{Max: 30, Window: time.Minute}, l.Tool)
}

// Every limited route answers 429 + Retry-After + the JSON error once the budget
// is spent, and the refused request never reaches the handler.
func TestLimiter_EveryRouteRefusesPastItsBudget(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		set                      func(*dataapi.Limits)
	}{
		{"fits search", "GET", "/v1/fits/search", "", func(l *dataapi.Limits) { l.FitsSearch = dataapi.Rate{Max: 2, Window: time.Minute} }},
		{"fits detail", "POST", "/v1/fits/detail", `{"eft":"[Raven, T]"}`, func(l *dataapi.Limits) { l.FitsDetail = dataapi.Rate{Max: 2, Window: time.Minute} }},
		{"fit stats", "POST", "/v1/fit/stats", `{"eft":"[Rifter, x]"}`, func(l *dataapi.Limits) { l.FitStats = dataapi.Rate{Max: 2, Window: time.Minute} }},
		{"fit suggest", "POST", "/v1/fit/suggest", `{"eft":"[Gila, x]","slot":"high"}`, func(l *dataapi.Limits) { l.FitSuggest = dataapi.Rate{Max: 2, Window: time.Minute} }},
		{"items search", "GET", "/v1/items/search?q=repairer", "", func(l *dataapi.Limits) { l.ItemsSearch = dataapi.Rate{Max: 2, Window: time.Minute} }},
		{"tool", "POST", "/v1/tool/no_such_tool", `{}`, func(l *dataapi.Limits) { l.Tool = dataapi.Rate{Max: 2, Window: time.Minute} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t, dataapi.Config{Limits: limitsOf(tc.set), ToolAPI: dataapi.ToolAPIPublic})
			for i := 0; i < 2; i++ {
				rec := do(a, tc.method, tc.path, tc.body)
				require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "request %d is within budget", i+1)
			}
			rec := do(a, tc.method, tc.path, tc.body)
			requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
			secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
			require.NoError(t, err, "Retry-After is whole seconds")
			require.InDelta(t, 60, secs, 1)
		})
	}
}

// Budgets are per route: spending one does not eat another.
func TestLimiter_RoutesHaveSeparateBudgets(t *testing.T) {
	a := newAPI(t, dataapi.Config{Limits: limitsOf(func(l *dataapi.Limits) {
		l.FitsSearch = dataapi.Rate{Max: 1, Window: time.Minute}
		l.ItemsSearch = dataapi.Rate{Max: 1, Window: time.Minute}
	})})
	require.Equal(t, http.StatusServiceUnavailable, do(a, "GET", "/v1/fits/search", "").Code)
	require.Equal(t, http.StatusTooManyRequests, do(a, "GET", "/v1/fits/search", "").Code)
	require.Equal(t, http.StatusServiceUnavailable, do(a, "GET", "/v1/items/search?q=ab", "").Code)
}

func TestLimiter_DisabledRateIsUnlimited(t *testing.T) {
	a := newAPI(t, dataapi.Config{Limits: limitsOf(func(l *dataapi.Limits) { l.FitsSearch = dataapi.Rate{} })})
	for i := 0; i < 300; i++ {
		require.Equal(t, http.StatusServiceUnavailable, do(a, "GET", "/v1/fits/search", "").Code)
	}
}

func TestLimiter_HealthIsNeverLimited(t *testing.T) {
	a := newAPI(t, dataapi.Config{})
	for i := 0; i < 500; i++ {
		require.Equal(t, http.StatusOK, do(a, "GET", "/v1/health", "").Code)
	}
}

func oneRequestBudget() dataapi.Config {
	return dataapi.Config{Limits: limitsOf(func(l *dataapi.Limits) { l.ItemsSearch = dataapi.Rate{Max: 1, Window: time.Minute} })}
}

const itemsURL = "/v1/items/search?q=repairer"

// Behind the Cloudflare tunnel every request shares the tunnel's address;
// CF-Connecting-IP separates visitors.
func TestLimiter_KeyedByCFConnectingIP(t *testing.T) {
	a := newAPI(t, oneRequestBudget())
	const tunnel = "172.18.0.5:40000"
	require.Equal(t, http.StatusServiceUnavailable, doFrom(a, tunnel, "GET", itemsURL, "", "CF-Connecting-IP", "203.0.113.50").Code)
	require.Equal(t, http.StatusTooManyRequests, doFrom(a, tunnel, "GET", itemsURL, "", "CF-Connecting-IP", "203.0.113.50").Code)
	// Another visitor through the same tunnel address is unaffected.
	require.Equal(t, http.StatusServiceUnavailable, doFrom(a, tunnel, "GET", itemsURL, "", "CF-Connecting-IP", "203.0.113.51").Code)
}

// The regression R0.4 fixed: a caller must not escape its bucket by rotating
// X-Forwarded-For / X-Real-IP / True-Client-IP.
func TestLimiter_NotBypassedBySpoofedForwardingHeaders(t *testing.T) {
	a := newAPI(t, oneRequestBudget())
	const peer = "198.51.100.24:5555"
	var last *httptest.ResponseRecorder
	for i := 0; i < 5; i++ {
		ip := "10.0.0." + strconv.Itoa(i+1)
		last = doFrom(a, peer, "GET", itemsURL, "", "X-Forwarded-For", ip, "X-Real-IP", ip, "True-Client-IP", ip)
	}
	require.Equal(t, http.StatusTooManyRequests, last.Code)
}

// Without the CF header the TCP peer is the key, and the source port is not part
// of it (every new connection from one client shares a bucket).
func TestLimiter_FallsBackToTCPPeerIgnoringPort(t *testing.T) {
	a := newAPI(t, oneRequestBudget())
	require.Equal(t, http.StatusServiceUnavailable, doFrom(a, "198.51.100.1:1000", "GET", itemsURL, "").Code)
	require.Equal(t, http.StatusTooManyRequests, doFrom(a, "198.51.100.1:2000", "GET", itemsURL, "").Code)
	require.Equal(t, http.StatusServiceUnavailable, doFrom(a, "198.51.100.2:1000", "GET", itemsURL, "").Code)
}

type headerKeys struct{}

func (headerKeys) Key(r *http.Request) string { return "key:" + r.Header.Get("X-Test-Key") }

// The per-key hook: a resolver replaces IP keying wholesale.
func TestLimiter_CustomKeyResolver(t *testing.T) {
	cfg := oneRequestBudget()
	cfg.Keys = headerKeys{}
	a := newAPI(t, cfg)
	// Same IP, different keys → independent buckets.
	require.Equal(t, http.StatusServiceUnavailable, do(a, "GET", itemsURL, "", "X-Test-Key", "alpha").Code)
	require.Equal(t, http.StatusServiceUnavailable, do(a, "GET", itemsURL, "", "X-Test-Key", "beta").Code)
	require.Equal(t, http.StatusTooManyRequests, do(a, "GET", itemsURL, "", "X-Test-Key", "alpha").Code)
	// Different IPs, same key → one shared bucket.
	require.Equal(t, http.StatusTooManyRequests, doFrom(a, "203.0.113.200:1", "GET", itemsURL, "", "X-Test-Key", "alpha").Code)
}

// A 429 is returned before the handler runs, so a limited caller cannot make the
// corpus work.
func TestLimiter_RefusedRequestNeverReachesTheHandler(t *testing.T) {
	stub := &stubSearcher{res: rag.FitSearchResult{}}
	a := newAPI(t, dataapi.Config{
		Deps:   dataapi.Deps{Retriever: stub},
		Limits: limitsOf(func(l *dataapi.Limits) { l.FitsSearch = dataapi.Rate{Max: 1, Window: time.Minute} }),
	})
	require.Equal(t, http.StatusOK, do(a, "GET", "/v1/fits/search?q=a", "").Code)
	require.Equal(t, http.StatusTooManyRequests, do(a, "GET", "/v1/fits/search?q=b", "").Code)
	require.Len(t, stub.queries(), 1)
}

// --- tool API: opt-in, public vs loopback -----------------------------------

func emptyToolDeps() dataapi.Deps { return dataapi.Deps{Tools: &tools.Deps{}} }

func TestToolAPI_OffByDefaultMeansNoRoute(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps()})
	requireError(t, do(a, "POST", "/v1/tool/get_jumps_between", `{}`), http.StatusNotFound, "not_found")
	requireError(t, do(a, "GET", "/v1/tool/get_jumps_between", ""), http.StatusNotFound, "not_found")
}

func TestToolAPI_LoopbackRunsEveryTierWithoutKeys(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	for _, name := range []string{"appraise_items", "get_fits", "list_fits", "get_market_price"} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, http.StatusOK, do(a, "POST", "/v1/tool/"+name, `{}`).Code)
		})
	}
}

// Public: 30 requests/min per client IP by default; a different client keeps its
// own budget.
func TestToolAPI_PublicRateLimitedPerClientIP30PerMinute(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic})
	const addr = "198.51.100.12:2222"
	for i := 0; i < 30; i++ {
		rec := doFrom(a, addr, "POST", "/v1/tool/no_such_tool", `{}`)
		require.Equal(t, http.StatusNotFound, rec.Code, "request %d is within the 30/min budget", i+1)
	}
	requireError(t, doFrom(a, addr, "POST", "/v1/tool/no_such_tool", `{}`), http.StatusTooManyRequests, "rate_limited")
	require.Equal(t, http.StatusNotFound, doFrom(a, "198.51.100.13:2222", "POST", "/v1/tool/no_such_tool", `{}`).Code)
}

func TestToolAPI_LoopbackIsNotRateLimited(t *testing.T) {
	a := newAPI(t, dataapi.Config{Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPILoopback})
	for i := 0; i < 40; i++ {
		require.Equal(t, http.StatusNotFound, do(a, "POST", "/v1/tool/no_such_tool", `{}`).Code, "request %d", i+1)
	}
}
