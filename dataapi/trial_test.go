package dataapi_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// Without an API key the hosted API is for trying it out: trial limits. A key is for real
// use. These tests pin the numbers of the policy and the rule that they only apply where
// keys exist.

const trialKey = "ecd_trial_test_key"

// stubMCP answers 200 to anything: the MCP transport itself is not under test here.
var stubMCP = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// allowedBeforeRefusal sends up to n requests built by call and returns how many were not 429
// before the first refused one, which is returned too (nil when none was refused).
func allowedBeforeRefusal(n int, call func() *httptest.ResponseRecorder) (allowed int, refused *httptest.ResponseRecorder) {
	for range n {
		rec := call()
		if rec.Code == http.StatusTooManyRequests {
			return allowed, rec
		}
		allowed++
	}
	return allowed, nil
}

func TestDefaultLimits_CarryTheTrialAndKeyedNumbers(t *testing.T) {
	l := dataapi.DefaultLimits()
	require.Equal(t, dataapi.Rate{Max: 20, KeyedMax: 120, Window: time.Minute}, l.MCP)
	require.Equal(t, dataapi.Rate{Max: 10, KeyedMax: 60, Window: time.Minute}, l.Tool)
	require.Equal(t, dataapi.Rate{Max: 30, KeyedMax: 240, Window: time.Minute}, l.FitsSearch)
	require.Equal(t, dataapi.Rate{Max: 5, KeyedMax: 10, Window: time.Minute}, l.UpstreamTool)
	// every other route keeps today's numbers and the factor
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitsDetail)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitStats)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitSuggest)
	require.Equal(t, dataapi.Rate{Max: 120, Window: time.Minute}, l.ItemsSearch)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.Docs)
}

func TestLimitsWithoutKeys_ServeEveryoneTheKeyedNumbers(t *testing.T) {
	l := dataapi.DefaultLimits().WithoutKeys()
	require.Equal(t, dataapi.Rate{Max: 120, Window: time.Minute}, l.MCP)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.Tool)
	require.Equal(t, dataapi.Rate{Max: 240, Window: time.Minute}, l.FitsSearch)
	require.Equal(t, dataapi.Rate{Max: 10, Window: time.Minute}, l.UpstreamTool)
	require.Equal(t, dataapi.Rate{Max: 60, Window: time.Minute}, l.FitsDetail, "a route without a keyed figure is unchanged")
}

// trialAPI serves every metered route with the default (trial) limits, keys on, no daily
// cap, and a stub search corpus and MCP handler.
func trialAPI(t *testing.T) *dataapi.API {
	t.Helper()
	return newAPI(t, dataapi.Config{
		Deps:             dataapi.Deps{Tools: &tools.Deps{}, Retriever: &stubSearcher{}},
		ToolAPI:          dataapi.ToolAPIPublic,
		MCP:              stubMCP,
		Auth:             keyedFixture(t, trialKey),
		KeyedLimitFactor: 2,
	})
}

func TestTrialLimits_KeylessAndKeyedPerMinute(t *testing.T) {
	cases := []struct {
		name           string
		trial, keyed   int
		call           func(api *dataapi.API, hdr ...string) *httptest.ResponseRecorder
		wantStatusPass int
	}{
		{"mcp", 20, 120, func(api *dataapi.API, hdr ...string) *httptest.ResponseRecorder {
			return do(api, http.MethodPost, "/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, hdr...)
		}, http.StatusOK},
		{"tool", 10, 60, func(api *dataapi.API, hdr ...string) *httptest.ResponseRecorder {
			return do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, hdr...)
		}, http.StatusOK},
		{"fits search", 30, 240, func(api *dataapi.API, hdr ...string) *httptest.ResponseRecorder {
			return do(api, http.MethodGet, "/v1/fits/search?ship=Raven", "", hdr...)
		}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := trialAPI(t)

			allowed, refused := allowedBeforeRefusal(tc.trial+5, func() *httptest.ResponseRecorder {
				return tc.call(api, fromIP("203.0.113.9")...)
			})
			require.Equal(t, tc.trial, allowed, "a keyless caller gets the trial budget")
			requireError(t, refused, http.StatusTooManyRequests, "rate_limited")

			hdr := append(fromIP("203.0.113.9"), "X-API-Key", trialKey)
			allowed, refused = allowedBeforeRefusal(tc.keyed+5, func() *httptest.ResponseRecorder { return tc.call(api, hdr...) })
			require.Equal(t, tc.keyed, allowed, "a key gets its own, larger budget, in its own bucket")
			requireError(t, refused, http.StatusTooManyRequests, "rate_limited")
		})
	}
}

func TestTrialLimits_LiveESIToolsFiveKeylessTenKeyed(t *testing.T) {
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{} // the route limit must not interfere
	api := newAPI(t, dataapi.Config{
		Deps:     emptyToolDeps(),
		ToolAPI:  dataapi.ToolAPIPublic,
		Auth:     keyedFixture(t, trialKey),
		Limits:   &limits,
		Upstream: dataapi.NewUpstreamLimiter(limits.UpstreamTool, nil),
	})
	call := func(hdr ...string) *httptest.ResponseRecorder {
		return do(api, http.MethodPost, "/v1/tool/get_market_price", `{"type_id":0}`, hdr...)
	}
	allowed, refused := allowedBeforeRefusal(20, func() *httptest.ResponseRecorder { return call(fromIP("198.51.100.5")...) })
	require.Equal(t, 5, allowed)
	requireError(t, refused, http.StatusTooManyRequests, "rate_limited")

	allowed, refused = allowedBeforeRefusal(20, func() *httptest.ResponseRecorder { return call("X-API-Key", trialKey) })
	require.Equal(t, 10, allowed)
	requireError(t, refused, http.StatusTooManyRequests, "rate_limited")
}

func TestServiceWithoutKeys_ServesEveryoneTheFullNumbers(t *testing.T) {
	api := newAPI(t, dataapi.Config{
		Deps:         dataapi.Deps{Tools: &tools.Deps{}, Retriever: &stubSearcher{}},
		ToolAPI:      dataapi.ToolAPIPublic,
		MCP:          stubMCP,
		AnonDailyCap: 3, // ignored: nobody can get a key here
	})
	mcp := func() *httptest.ResponseRecorder {
		return do(api, http.MethodPost, "/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, fromIP("203.0.113.9")...)
	}
	allowed, refused := allowedBeforeRefusal(200, mcp)
	require.Equal(t, 120, allowed, "no key to get: the keyed MCP number for everyone, and no daily cap")
	requireError(t, refused, http.StatusTooManyRequests, "rate_limited")

	tool := func() *httptest.ResponseRecorder {
		return do(api, http.MethodPost, "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("203.0.113.10")...)
	}
	allowed, _ = allowedBeforeRefusal(100, tool)
	require.Equal(t, 60, allowed)

	search := func() *httptest.ResponseRecorder {
		return do(api, http.MethodGet, "/v1/fits/search?q=armor+tank", "", fromIP("203.0.113.11")...)
	}
	allowed, _ = allowedBeforeRefusal(300, search)
	require.Equal(t, 240, allowed, "q is open where there are no keys, at the keyed number")
}

// --- anonymous daily cap -------------------------------------------------------------

// clock is an injectable UTC clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func capAPI(t *testing.T, cap int, publicURL string, c *clock, auth dataapi.Authenticator) *dataapi.API {
	t.Helper()
	// Per-minute limits off, so only the daily cap can refuse.
	return newAPI(t, dataapi.Config{
		Docs:         true,
		PublicURL:    publicURL,
		Auth:         auth,
		Limits:       &dataapi.Limits{},
		AnonDailyCap: cap,
		Now:          c.now,
	})
}

// counted is a request every counting rule applies to: an unknown path (404).
func counted(api http.Handler, hdr ...string) *httptest.ResponseRecorder {
	return do(api, http.MethodGet, "/v1/no/such/route", "", hdr...)
}

func TestAnonDailyCap_The201stRequestIsRefusedUntilMidnightUTC(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 22, 30, 0, 0, time.UTC)}
	api := capAPI(t, 200, "https://data.example", c, keyedFixture(t, trialKey))
	for i := range 200 {
		require.Equal(t, http.StatusNotFound, counted(api, fromIP("203.0.113.9")...).Code, "request %d", i+1)
	}
	rec := counted(api, fromIP("203.0.113.9")...)
	requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Equal(t, strconv.Itoa(90*60), rec.Header().Get("Retry-After"), "seconds to 00:00 UTC")
	require.Contains(t, rec.Body.String(),
		"without an API key the API allows 200 requests a day per client; a free key raises this: https://data.example/docs/access#access-keys")

	// another client has its own count
	require.Equal(t, http.StatusNotFound, counted(api, fromIP("203.0.113.10")...).Code)
}

func TestAnonDailyCap_MessageWithoutAPublicURLOmitsTheLink(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := capAPI(t, 1, "", c, keyedFixture(t, trialKey))
	counted(api)
	rec := counted(api)
	requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Contains(t, rec.Body.String(), "without an API key the API allows 1 requests a day per client; a free key raises this")
	require.NotContains(t, rec.Body.String(), "http")
}

func TestAnonDailyCap_ResetsWhenTheUTCDayChanges(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 23, 59, 0, 0, time.UTC)}
	api := capAPI(t, 2, "", c, keyedFixture(t, trialKey))
	counted(api)
	counted(api)
	require.Equal(t, http.StatusTooManyRequests, counted(api).Code)
	c.t = time.Date(2026, 10, 14, 0, 0, 1, 0, time.UTC)
	require.Equal(t, http.StatusNotFound, counted(api).Code, "a new UTC day starts from zero")
	require.Equal(t, http.StatusNotFound, counted(api).Code)
	require.Equal(t, http.StatusTooManyRequests, counted(api).Code)
}

func TestAnonDailyCap_BrowsingTheDocsNeverSpendsTheTrial(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := capAPI(t, 2, "", c, keyedFixture(t, trialKey))
	for _, path := range []string{
		"/", "/docs", "/docs/", "/docs/access", "/docs/tools", "/docs/tools/get_jumps_between", "/docs/search.json",
		"/terms", "/terms.md", "/llms.txt", "/llms-full.txt", "/v1/openapi.yaml", "/v1/openapi.json", "/v1/health",
	} {
		for range 5 {
			rec := do(api, http.MethodGet, path, "")
			require.NotEqual(t, http.StatusTooManyRequests, rec.Code, path)
		}
	}
	require.Equal(t, http.StatusNotFound, counted(api).Code, "the trial is untouched")
	require.Equal(t, http.StatusNotFound, counted(api).Code)
	require.Equal(t, http.StatusTooManyRequests, counted(api).Code)
}

func TestAnonDailyCap_AKeyedCallerIsNeverCounted(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := capAPI(t, 1, "", c, keyedFixture(t, trialKey))
	for range 20 {
		require.Equal(t, http.StatusNotFound, counted(api, "X-API-Key", trialKey).Code)
	}
	// an invalid key is an anonymous caller: counted by IP
	counted(api, "X-API-Key", "nope")
	require.Equal(t, http.StatusTooManyRequests, counted(api, "X-API-Key", "nope").Code)
}

func TestAnonDailyCap_ZeroIsOff(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := capAPI(t, 0, "", c, keyedFixture(t, trialKey))
	for range 300 {
		require.Equal(t, http.StatusNotFound, counted(api).Code)
	}
}

func TestAnonDailyCap_NeedsKeysToExist(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	api := capAPI(t, 1, "", c, nil)
	for range 5 {
		require.Equal(t, http.StatusNotFound, counted(api).Code, "a service without keys has no daily cap")
	}
}

// --- free-text fit search needs a key ------------------------------------------------

func TestFitsSearch_QNeedsAKeyWhereKeysExist(t *testing.T) {
	st := &stubSearcher{}
	api := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: st}, Auth: keyedFixture(t, trialKey)})

	rec := do(api, http.MethodGet, "/v1/fits/search?q=cheap+armor+raven", "")
	requireError(t, rec, http.StatusUnauthorized, "api_key_required")
	require.Contains(t, rec.Body.String(), "free-text fit search (q) needs an API key; the filters work without one")
	require.Empty(t, st.queries(), "the corpus was not asked")

	// an empty q is not a query
	rec = do(api, http.MethodGet, "/v1/fits/search?q=&ship=Raven", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// filters work keyless
	rec = do(api, http.MethodGet, "/v1/fits/search?ship=Raven&activity=pve", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// a key may use q
	rec = do(api, http.MethodGet, "/v1/fits/search?q=cheap+armor+raven", "", "X-API-Key", trialKey)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	qs := st.queries()
	require.Len(t, qs, 3)
	require.Equal(t, "cheap armor raven", qs[2].Q)

	// a key that was presented but is wrong is told so, not treated as anonymous
	rec = do(api, http.MethodGet, "/v1/fits/search?q=x", "", "X-API-Key", "nope")
	requireError(t, rec, http.StatusUnauthorized, "invalid_api_key")
}

func TestFitsSearch_QIsOpenOnAServiceWithoutKeys(t *testing.T) {
	st := &stubSearcher{}
	api := newAPI(t, dataapi.Config{Deps: dataapi.Deps{Retriever: st}})
	rec := do(api, http.MethodGet, "/v1/fits/search?q=cheap+armor+raven", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, st.queries(), 1)
}

// --- fit lookups are public ----------------------------------------------------------

func TestFitLookups_PublicOverRESTAndMCPButBattleAnalysisKeyed(t *testing.T) {
	api := trialAPI(t)
	for _, name := range []string{"get_fits", "list_fits"} {
		rec := do(api, http.MethodPost, "/v1/tool/"+name, `{"ship":"Raven","source":"x"}`, fromIP("203.0.113.40")...)
		require.NotEqual(t, http.StatusUnauthorized, rec.Code, name+": "+rec.Body.String())
		require.NotEqual(t, http.StatusForbidden, rec.Code, name)
	}
	rec := do(api, http.MethodPost, "/v1/tool/analyze_battle", `{"url":"https://zkillboard.com/related/1/1/"}`, fromIP("203.0.113.40")...)
	requireError(t, rec, http.StatusUnauthorized, "api_key_required")
}

func TestFitLookups_ListedInKeylessMCPToolsList(t *testing.T) {
	deps := &tools.Deps{}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), Auth: keyedFixture(t, trialKey), Limits: &dataapi.Limits{},
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{DisableLocalhostProtection: true}),
	})
	listed := listedTools(t, api)
	require.Contains(t, listed, "get_fits")
	require.Contains(t, listed, "list_fits")
	require.NotContains(t, listed, "analyze_battle")
}

func TestMCPInstructions_KeylessCallersAreToldItIsATrial(t *testing.T) {
	deps := &tools.Deps{}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), Auth: keyedFixture(t, trialKey), Limits: &dataapi.Limits{},
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true, AnonDailyCap: 200,
			Keyed:   func(r *http.Request) bool { return dataapi.CallerFrom(r.Context()).Allows(catalog.TierKeyed) },
			KeyHint: "https://data.example/docs/access#access-keys",
		}),
	})
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	rec := postMCP(t, api, init)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Without an API key this server is for trying it out: 200 requests a day per client and lower per-minute limits; analyze_battle and free-text fit search need a key. A free key: https://data.example/docs/access#access-keys")

	rec = postMCP(t, api, init, "X-API-Key", trialKey)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "trying it out")
}

// --- the documents state both tiers ---------------------------------------------------

func TestDocs_StateTrialAndKeyedNumbersAndTheKeyOnlySearch(t *testing.T) {
	files, err := dataapi.GeneratedFiles()
	require.NoError(t, err)
	full := string(files["llms-full.txt"])
	require.Contains(t, full, "Rate limit: 20 requests per minute per client IP without an API key, 120 per minute per API key with one.")
	require.Contains(t, full, "Rate limit: 30 requests per minute per client IP without an API key, 240 per minute per API key with one.")
	require.Contains(t, full, "200 requests per UTC day per client")
	require.Contains(t, full, "Free-text search (`q`) needs an API key")
	require.Contains(t, string(files["api/openapi.yaml"]), "Free-text search (`q`) needs an API key")

	// a service without keys states one number per route and no key-only search
	s := dataapi.PublicSurface()
	s.Auth, s.SelfServe, s.AnonDailyCap = false, false, 0
	s.Limits = s.Limits.WithoutKeys()
	nokeys := dataapi.LLMsFullTxt(s)
	require.Contains(t, nokeys, "Rate limit: 120 requests per minute per caller")
	require.NotContains(t, nokeys, "without an API key")
	require.NotContains(t, nokeys, "needs an API key")
}

func TestPortal_AccessPageStatesTheTrialAndTheKeyedLimits(t *testing.T) {
	api := newAPI(t, publicConfig())
	access := do(api, http.MethodGet, "/docs/access", "").Body.String()
	require.Contains(t, access, "daily trial limit of 200 requests per client")
	require.Contains(t, access, "20 per minute</td><td class=\"num\">120 per minute")
	require.Contains(t, access, "5 calls per minute per caller without a key and 10 with one")

	idx := do(api, http.MethodGet, "/", "", "Accept", "application/json").Body.String()
	require.Regexp(t, `"max": 20,\s+"max_with_key": 120`, idx)
}

// Two addresses of one IPv6 /64 are one client for the daily cap; another /64 and an IPv4 are not.
func TestAnonDailyCap_IPv6SlashSixtyFourIsOneClient(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 22, 30, 0, 0, time.UTC)}
	api := capAPI(t, 4, "https://data.example", c, keyedFixture(t, trialKey))
	for i := range 4 {
		addr := "2001:db8:1:2::" + strconv.Itoa(i+1)
		require.Equal(t, http.StatusNotFound, counted(api, fromIP(addr)...).Code, "request %d", i+1)
	}
	requireError(t, counted(api, fromIP("2001:db8:1:2:ffff::7")...), http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Equal(t, http.StatusNotFound, counted(api, fromIP("2001:db8:1:3::1")...).Code)
	require.Equal(t, http.StatusNotFound, counted(api, fromIP("203.0.113.9")...).Code)
}

// --- the range tier's daily cap ------------------------------------------------------

// capEcho is an MCP handler that answers the daily cap the request's caller has
// (dataapi.AnonDailyCapFor), the number mcpserver states in the keyless instructions.
var capEcho = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(strconv.Itoa(dataapi.AnonDailyCapFor(r))))
})

// rangeCapAPI serves the counted routes and capEcho with API keys, the per-minute limits off,
// the anonymous daily cap anonCap, the range tier's daily cap rangeCap and the ranges ("" none).
func rangeCapAPI(t *testing.T, anonCap, rangeCap int, ranges string, c *clock) *dataapi.API {
	t.Helper()
	return newAPI(t, dataapi.Config{
		PublicURL: "https://data.example", Auth: keyedFixture(t, trialKey), MCP: capEcho,
		Limits:       &dataapi.Limits{Range: dataapi.RangeLimits{DailyCap: rangeCap}},
		AnonDailyCap: anonCap, Now: c.now, AnthropicRanges: mustRanges(t, ranges),
	})
}

// dailyCapOf is the daily cap the API gives the caller of hdr (capEcho).
func dailyCapOf(t *testing.T, api http.Handler, hdr ...string) string {
	t.Helper()
	rec := do(api, http.MethodPost, "/v1/mcp", `{}`, hdr...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// countedUntilRefused makes up to 50 counted requests with hdr and returns how many were served
// before the first refusal, which must be the daily cap's.
func countedUntilRefused(t *testing.T, api http.Handler, hdr ...string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	allowed, refused := allowedBeforeRefusal(50, func() *httptest.ResponseRecorder { return counted(api, hdr...) })
	require.NotNil(t, refused, "no refusal in 50 requests")
	requireError(t, refused, http.StatusTooManyRequests, "daily_quota_exceeded")
	return allowed, refused
}

// With the default limits, a keyless address inside the Anthropic ranges has 2,000 requests a
// UTC day; every other keyless caller keeps the ordinary cap. The cap is enforced at the number
// the caller is given (small injected caps, so the test makes no 2,000 requests).
func TestAnonDailyCap_RangeAddressGetsTwoThousand(t *testing.T) {
	require.Equal(t, 2000, dataapi.DefaultLimits().Range.DailyCap)
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	api := newAPI(t, dataapi.Config{
		Auth: keyedFixture(t, trialKey), MCP: capEcho, AnonDailyCap: dataapi.DefaultAnonDailyCap, Now: c.now,
		AnthropicRanges: mustRanges(t, anthropicOutbound), // Limits nil: DefaultLimits
	})
	require.Equal(t, "2000", dailyCapOf(t, api, fromIP("160.79.104.9")...))
	require.Equal(t, "2000", dailyCapOf(t, api, fromIP("153.61.223.250")...))
	require.Equal(t, "200", dailyCapOf(t, api, fromIP("192.0.2.9")...), "outside the ranges: the ordinary cap")
	require.Equal(t, "200", dailyCapOf(t, api, fromIP("2001:db8::9")...))

	api = rangeCapAPI(t, 3, 5, anthropicOutbound, c)
	n, _ := countedUntilRefused(t, api, fromIP("160.79.104.9")...)
	require.Equal(t, 5, n, "a range address is refused past the range cap")
	n, _ = countedUntilRefused(t, api, fromIP("192.0.2.9")...)
	require.Equal(t, 3, n, "an address outside is refused past the ordinary cap")
}

// A range caller has the larger of the range tier's daily cap and the anonymous daily cap
// (owner ruling 2026-11-12): an operator who raises DATAAPI_ANON_DAILY_CAP above the range cap
// never gives Anthropic's connectors less than everyone else.
func TestAnonDailyCap_RangeCallerGetsTheLargerCap(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	for _, tc := range []struct {
		anon int
		want string
	}{{5000, "5000"}, {200, "2000"}} {
		api := newAPI(t, dataapi.Config{
			Auth: keyedFixture(t, trialKey), MCP: capEcho, AnonDailyCap: tc.anon, Now: c.now,
			AnthropicRanges: mustRanges(t, anthropicOutbound), // Limits nil: DefaultLimits
		})
		require.Equal(t, tc.want, dailyCapOf(t, api, fromIP("160.79.104.9")...), "anon cap %d", tc.anon)
		require.Equal(t, strconv.Itoa(tc.anon), dailyCapOf(t, api, fromIP("192.0.2.9")...), "outside the ranges")
	}
}

// The 429 of the daily cap states the number the refused caller really has, and the wait to
// 00:00 UTC.
func TestAnonDailyCap_RangeRefusalStatesItsOwnCap(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 22, 30, 0, 0, time.UTC)}
	api := rangeCapAPI(t, 3, 5, anthropicOutbound, c)
	_, rec := countedUntilRefused(t, api, fromIP("153.61.208.9")...)
	require.Equal(t, strconv.Itoa(90*60), rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(),
		"without an API key the API allows 5 requests a day per client; a free key raises this: https://data.example/docs/access#access-keys")
	_, rec = countedUntilRefused(t, api, fromIP("192.0.2.9")...)
	require.Contains(t, rec.Body.String(), "without an API key the API allows 3 requests a day per client; a free key raises this")
}

// Each range address has its own count; a caller with a key is never counted, inside the ranges
// as elsewhere; an invalid key is a keyless caller of its address.
func TestAnonDailyCap_RangeAddressesHaveSeparateCounters(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	api := rangeCapAPI(t, 1, 2, anthropicOutbound, c)
	for range 10 {
		require.Equal(t, http.StatusNotFound, counted(api, append(fromIP("160.79.104.9"), "X-API-Key", trialKey)...).Code)
	}
	for _, ip := range []string{"160.79.104.9", "160.79.104.10", "153.61.208.9"} {
		n, _ := countedUntilRefused(t, api, fromIP(ip)...)
		require.Equal(t, 2, n, ip)
	}
	for _, ip := range []string{"192.0.2.9", "192.0.2.10"} {
		n, _ := countedUntilRefused(t, api, fromIP(ip)...)
		require.Equal(t, 1, n, ip)
	}
	n, _ := countedUntilRefused(t, api, append(fromIP("160.79.104.11"), "X-API-Key", "nope")...)
	require.Equal(t, 2, n, "an invalid key is counted by its address")
}

// Inside a range, two addresses of one IPv6 /64 are still one client for the range cap; another
// /64 of the range is another client.
func TestAnonDailyCap_RangeIPv6SlashSixtyFourIsOneClient(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	api := rangeCapAPI(t, 1, 4, "2001:db8::/32", c)
	require.Equal(t, "4", dailyCapOf(t, api, fromIP("2001:db8:9::1")...))
	for i := range 4 {
		require.Equal(t, http.StatusNotFound, counted(api, fromIP("2001:db8:1:2::"+strconv.Itoa(i+1))...).Code, "request %d", i+1)
	}
	rec := counted(api, fromIP("2001:db8:1:2:ffff::7")...)
	requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Contains(t, rec.Body.String(), "allows 4 requests a day per client")
	n, _ := countedUntilRefused(t, api, fromIP("2001:db8:1:3::1")...)
	require.Equal(t, 4, n)
	n, _ = countedUntilRefused(t, api, fromIP("192.0.2.9")...)
	require.Equal(t, 1, n, "an IPv4 address outside the range")
}

// The range cap exists only with the range tier and the anonymous daily cap: without ranges or
// with a zero range cap a range address has the ordinary cap, and with the anonymous cap off
// nobody has a daily cap.
func TestAnonDailyCap_RangeCapOnlyWithTheTier(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	n, _ := countedUntilRefused(t, rangeCapAPI(t, 2, 5, "", c), fromIP("160.79.104.9")...)
	require.Equal(t, 2, n, "no ranges")
	n, _ = countedUntilRefused(t, rangeCapAPI(t, 2, 0, anthropicOutbound, c), fromIP("160.79.104.9")...)
	require.Equal(t, 2, n, "a zero range cap")

	off := rangeCapAPI(t, 0, 5, anthropicOutbound, c)
	require.Equal(t, "0", dailyCapOf(t, off, fromIP("160.79.104.9")...))
	for range 20 {
		require.Equal(t, http.StatusNotFound, counted(off, fromIP("160.79.104.9")...).Code, "the anonymous cap is off")
	}

	nokeys := newAPI(t, dataapi.Config{
		MCP: capEcho, Limits: &dataapi.Limits{Range: dataapi.RangeLimits{DailyCap: 5}}, AnonDailyCap: 2, Now: c.now,
		AnthropicRanges: mustRanges(t, anthropicOutbound),
	})
	require.Equal(t, "0", dailyCapOf(t, nokeys, fromIP("160.79.104.9")...))
	for range 20 {
		require.Equal(t, http.StatusNotFound, counted(nokeys, fromIP("160.79.104.9")...).Code, "no keys, no daily cap")
	}
}

// On a full route the check that stands in for the daily cap uses the caller's own cap too: a
// range address under its range cap (but over the ordinary one) is busy, one over its range cap
// gets the daily refusal (on /v1/mcp a range caller's, in JSON-RPC); an ordinary caller over its
// cap gets the daily 429 with its number.
func TestAnonDailyCap_FullRouteChecksTheRangeCap(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	build := func() (*dataapi.API, *holdMCP) {
		mcp := newHoldMCP()
		return newAPI(t, dataapi.Config{
			MCP: mcp, Auth: keyedFixture(t, trialKey), Limits: &dataapi.Limits{Range: dataapi.RangeLimits{DailyCap: 2}},
			AnonDailyCap: 1, Now: c.now, MaxInFlight: 1, PublicURL: "https://data.example",
			AnthropicRanges: mustRanges(t, anthropicOutbound),
		}), mcp
	}
	call := func(api http.Handler, ip string) *httptest.ResponseRecorder {
		return do(api, http.MethodPost, "/v1/mcp", mcpCall, fromIP(ip)...)
	}
	const x = "160.79.104.9"

	api, mcp := build()
	require.Equal(t, http.StatusOK, call(api, x).Code)
	require.Equal(t, http.StatusOK, call(api, "192.0.2.9").Code)
	release := hold(t, api, mcp, fromIP("192.0.2.200")...)
	requireError(t, call(api, x), http.StatusServiceUnavailable, "busy")
	rec := call(api, "192.0.2.9")
	requireError(t, rec, http.StatusTooManyRequests, "daily_quota_exceeded")
	require.Contains(t, rec.Body.String(), "allows 1 requests a day per client")
	require.Equal(t, http.StatusOK, release())
	require.Equal(t, http.StatusOK, call(api, x).Code, "the busy refusal spent nothing")

	api, mcp = build()
	for range 2 {
		require.Equal(t, http.StatusOK, call(api, x).Code)
	}
	release = hold(t, api, mcp, fromIP("192.0.2.200")...)
	defer release()
	// x spent the range cap of 2 (not the ordinary 1): a range caller's daily refusal of a
	// tools/call, answered in the protocol.
	rec = call(api, x)
	reply, _ := requireConnectorReply(t, rec, `1`, "the range cap at a full route")
	require.NotNil(t, reply.Result, rec.Body.String())
	require.True(t, reply.Result.IsError)
	require.Equal(t, "Today's request limit for Claude connectors is used up; it resets at 00:00 UTC. "+
		"Clients that can send an Authorization header can use a free API key: https://data.example/docs/access#access-keys", reply.Result.Content[0].Text)
}

// The keyless MCP instructions state the daily number of the caller who asks: 2,000 inside the
// ranges, the ordinary cap outside them, nothing about a trial for a caller with a key.
func TestMCPInstructions_RangeCallerSeesItsDailyNumber(t *testing.T) {
	deps := &tools.Deps{}
	api := newAPI(t, dataapi.Config{
		Deps: dataapi.NewDeps(deps, nil), Auth: keyedFixture(t, trialKey), AnonDailyCap: dataapi.DefaultAnonDailyCap,
		AnthropicRanges: mustRanges(t, anthropicOutbound),
		MCP: mcpserver.HTTPHandler(deps, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true, AnonDailyCapFor: dataapi.AnonDailyCapFor, Keyed: dataapi.KeyedTier,
			KeyHint: "https://data.example/docs/access#access-keys",
		}),
	})
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	rec := postMCP(t, api, init, fromIP("160.79.104.9")...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Without an API key this server is for trying it out: 2000 requests a day per client and lower per-minute limits;")

	rec = postMCP(t, api, init, fromIP("192.0.2.9")...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Without an API key this server is for trying it out: 200 requests a day per client and lower per-minute limits;")

	rec = postMCP(t, api, init, append(fromIP("160.79.104.9"), "X-API-Key", trialKey)...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "trying it out")
}
