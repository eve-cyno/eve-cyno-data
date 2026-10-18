package dataapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/tools"
)

// upstreamAPI serves the public tool API with a per-caller budget of max calls per minute
// for the live-ESI tools and a route limit that never interferes.
func upstreamAPI(t *testing.T, max int, deps *tools.Deps) *dataapi.API {
	t.Helper()
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{}
	limits.UpstreamTool = dataapi.Rate{Max: max, Window: time.Minute}
	log, _ := logSink()
	a := newAPI(t, dataapi.Config{
		Deps:     dataapi.Deps{Tools: deps},
		ToolAPI:  dataapi.ToolAPIPublic,
		Limits:   &limits,
		Upstream: dataapi.NewUpstreamLimiter(limits.UpstreamTool, nil),
		Logger:   log,
	})
	return a
}

func fromIP(ip string) []string { return []string{"CF-Connecting-IP", ip} }

func TestUpstreamTools_HaveAStricterPerIPLimitThanSDETools(t *testing.T) {
	a := upstreamAPI(t, 2, &tools.Deps{})

	for range 2 {
		rec := do(a, "POST", "/v1/tool/get_market_price", `{"type_id":0}`, fromIP("198.51.100.1")...)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	rec := do(a, "POST", "/v1/tool/get_market_price", `{"type_id":0}`, fromIP("198.51.100.1")...)
	requireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "get_market_price")

	// One budget for all four live tools, per caller: another tool of the same IP is refused
	// too, another IP is not.
	requireError(t, do(a, "POST", "/v1/tool/get_sovereignty", `{"system_name":"Jita"}`, fromIP("198.51.100.1")...), http.StatusTooManyRequests, "rate_limited")
	require.Equal(t, http.StatusOK, do(a, "POST", "/v1/tool/get_market_price", `{"type_id":0}`, fromIP("198.51.100.2")...).Code)

	// An SDE tool of the refused IP still runs: only the live tools have the lower limit.
	for range 5 {
		rec := do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("198.51.100.1")...)
		require.NotEqual(t, http.StatusTooManyRequests, rec.Code)
	}
}

func TestUpstreamLimiter_NilAndDisabledAllowEverything(t *testing.T) {
	var nilLimiter *dataapi.UpstreamLimiter
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ok, _ := nilLimiter.Allow(req, "get_market_price")
	require.True(t, ok)
	require.Nil(t, dataapi.NewUpstreamLimiter(dataapi.Rate{}, nil), "a disabled rate builds no limiter")
}

func TestUpstreamBusy_IsA503WithRetryAfterAndSendsNothing(t *testing.T) {
	var hits int
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "600")
		w.Header().Set("X-Ratelimit-Group", "g")
		w.Header().Set("X-Ratelimit-Limit", "600/15m")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(stub.Close)
	ec, err := esi.New(esi.Config{BaseURL: stub.URL, HTTPClient: stub.Client(), MaxWait: -1})
	require.NoError(t, err)
	deps := &tools.Deps{Client: &tools.Client{HTTP: stub.Client(), Throttle: tools.NewThrottle(), ESI: ec}}
	a := upstreamAPI(t, 100, deps)

	// The first call meets ESI's 429 and learns the back-off; the tool reports it as text.
	first := do(a, "POST", "/v1/tool/get_type_info", `{"type_id":587}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, 1, hits)

	// From then on the client refuses to send: 503 upstream_busy, never a 500.
	rec := do(a, "POST", "/v1/tool/get_type_info", `{"type_id":588}`)
	requireError(t, rec, http.StatusServiceUnavailable, "upstream_busy")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, 1, hits, "nothing was sent while backed off")
}

func TestAccessLog_CarriesAnOpaqueCallerTokenForToolRoutesOnly(t *testing.T) {
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	a := newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Limits: &limits, LogSalt: "test-salt", Logger: log,
	})
	do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("203.0.113.7")...)
	do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("203.0.113.7")...)
	do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("203.0.113.8")...)
	do(a, "GET", "/v1/health", "", fromIP("203.0.113.7")...)

	var callers []string
	var health map[string]any
	for _, line := range logLines(t, buf) {
		if line["msg"] != "request" {
			continue
		}
		if line["route"] == "GET /v1/health" {
			health = line
			continue
		}
		require.Equal(t, "get_jumps_between", line["tool"])
		callers = append(callers, line["caller"].(string))
	}
	require.Len(t, callers, 3)
	require.Equal(t, callers[0], callers[1], "the same IP gets the same token")
	require.NotEqual(t, callers[0], callers[2], "another IP gets another token")
	require.Regexp(t, `^[0-9a-f]{16}$`, callers[0])
	require.NotContains(t, buf.String(), "203.0.113", "no address reaches the log")
	require.NotContains(t, health, "caller", "only tool routes carry a caller token")
}

func TestGeneratedDocs_StateTheLiveToolsLimitAndBusyAnswer(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	full := string(files["llms-full.txt"])
	require.Contains(t, full, "fair-use limit of 5 calls per minute per caller without an API key and 10 with one")
	require.Contains(t, full, "upstream_busy")
	yaml := string(files["api/openapi.yaml"])
	require.Contains(t, yaml, "upstream_busy")
}

func TestGeneratedDocs_EveScoutToolNamesItsUpstream(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	full := string(files["llms-full.txt"])
	i := strings.Index(full, "\n### get_thera_connections\n")
	require.GreaterOrEqual(t, i, 0, "the tool has a section")
	section := full[i+1:]
	if j := strings.Index(section[4:], "\n### "); j >= 0 {
		section = section[:4+j]
	}
	require.Contains(t, section, "- Live EvE-Scout data: Reads live Thera and Turnur connections from EvE-Scout (Signal Cartel)")
	require.Contains(t, section, "follows its cache headers (one to ten minutes, five when they set none)", "the portal's cache wording")
	require.Contains(t, section, "it serves its last copy if that is at most an hour old, with the copy's time in `as_of`")
	require.Contains(t, section, "fair-use limit of 5 calls per minute per caller without an API key and 10 with one")
	require.NotContains(t, section, "about 5 minutes")
	require.NotContains(t, section, "upstream_busy")
	require.NotContains(t, section, "ESI's `Expires`")
}

func TestUpstreamBudget_PlanJumpRouteSpendsOnlyWithLiveOptions(t *testing.T) {
	a := upstreamAPI(t, 1, &tools.Deps{})
	call := func(body string) *httptest.ResponseRecorder {
		return do(a, "POST", "/v1/tool/plan_jump_route", body, fromIP("198.51.100.9")...)
	}
	plain := `{"origin":"Amamake","destination":"1DQ1-A","ship":"Rhea"}`
	live := `{"origin":"Amamake","destination":"1DQ1-A","ship":"Rhea","live_info":true}`
	require.Equal(t, http.StatusOK, call(plain).Code)
	require.Equal(t, http.StatusOK, call(plain).Code)
	require.Equal(t, http.StatusOK, call(live).Code)
	requireError(t, call(live), http.StatusTooManyRequests, "rate_limited")
	require.Equal(t, http.StatusOK, call(plain).Code, "a plain call after the budget is spent still runs")
}

func TestGeneratedDocs_PlanJumpRouteNamesItsLiveOptions(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	full := string(files["llms-full.txt"])
	i := strings.Index(full, "\n### plan_jump_route\n")
	require.GreaterOrEqual(t, i, 0, "the tool has a section")
	section := full[i+1:]
	if j := strings.Index(section[4:], "\n### "); j >= 0 {
		section = section[:4+j]
	}
	require.Contains(t, section, "Calls with `avoid_incursions` or `live_info` read live public data")
}

func TestGeneratedDocs_ListToolsStateTheirDataOnlyForTheListForm(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	yaml := string(files["api/openapi.yaml"])
	for _, typ := range []string{"SovereigntyList", "ActivityList"} {
		require.Contains(t, yaml, "`result.data` is a `"+typ+"` object when called with `systems` (null for the single `system_name` form).")
		require.NotContains(t, yaml, "`"+typ+"` object when the call succeeds")
	}
	require.NotContains(t, string(files["llms-full.txt"]), "Its own fair-use limit")
	require.Contains(t, string(files["llms-full.txt"]), "share one fair-use limit")
}
