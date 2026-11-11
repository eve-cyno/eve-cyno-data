package dataapi_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// tokenAPI serves the public tool API (route limits off, so nothing refuses) with the
// caller-token secret, the clock (nil: time.Now) and the authenticator (nil: none) given; its
// log goes to the returned buffer.
func tokenAPI(t *testing.T, secret string, now func() time.Time, auth dataapi.Authenticator) (*dataapi.API, *bytes.Buffer) {
	t.Helper()
	log, buf := logSink()
	limits := dataapi.DefaultLimits()
	limits.Tool = dataapi.Rate{}
	return newAPI(t, dataapi.Config{
		Deps: emptyToolDeps(), ToolAPI: dataapi.ToolAPIPublic, Limits: &limits, Auth: auth,
		LogSalt: secret, Now: now, Logger: log,
	}), buf
}

// lastToken returns the caller token of the last access line in buf.
func lastToken(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	lines := logLines(t, buf)
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i]["msg"] != "request" {
			continue
		}
		tok, ok := lines[i]["caller"].(string)
		require.True(t, ok, "the access line carries a caller token: %v", lines[i])
		require.Regexp(t, `^[0-9a-f]{16}$`, tok)
		return tok
	}
	t.Fatal("no access line")
	return ""
}

// tokenOf makes one tool call with the headers given and returns the caller token of its
// access line.
func tokenOf(t *testing.T, a *dataapi.API, buf *bytes.Buffer, hdr ...string) string {
	t.Helper()
	do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, hdr...)
	return lastToken(t, buf)
}

// wantToken is the caller token by its definition: the day key is HMAC-SHA256 of the domain
// string and the UTC date under the secret, the token HMAC-SHA256 of the caller key under the
// day key, cut to 8 bytes, in hex.
func wantToken(secret, day, key string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("eve-cyno dataapi caller token v1\x00" + day))
	m = hmac.New(sha256.New, m.Sum(nil))
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

func TestCallerToken_SameKeySameDayIsStable(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	a, buf := tokenAPI(t, "test-secret", c.now, nil)
	first := tokenOf(t, a, buf, fromIP("203.0.113.7")...)
	for _, at := range []time.Time{
		time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 12, 23, 59, 59, 999_999_999, time.UTC),
	} {
		c.t = at
		require.Equal(t, first, tokenOf(t, a, buf, fromIP("203.0.113.7")...), "same caller, same UTC day: %s", at)
	}
	require.NotEqual(t, first, tokenOf(t, a, buf, fromIP("203.0.113.8")...), "another caller, another token")

	// A restart within the day keeps the token: the secret keys it, not the process.
	b, bbuf := tokenAPI(t, "test-secret", c.now, nil)
	require.Equal(t, first, tokenOf(t, b, bbuf, fromIP("203.0.113.7")...))
}

func TestCallerToken_ChangesAtMidnightUTC(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 23, 59, 59, 999_999_999, time.UTC)}
	a, buf := tokenAPI(t, "test-secret", c.now, testKeys(t))
	before := tokenOf(t, a, buf, fromIP("203.0.113.7")...)
	keyedBefore := tokenOf(t, a, buf, append(fromIP("203.0.113.7"), "X-API-Key", keyAlpha)...)

	c.t = time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	after := tokenOf(t, a, buf, fromIP("203.0.113.7")...)
	require.NotEqual(t, before, after, "tokens never join across UTC days")
	require.NotEqual(t, keyedBefore, tokenOf(t, a, buf, append(fromIP("203.0.113.7"), "X-API-Key", keyAlpha)...), "a keyed caller's token rotates too")
	require.Equal(t, wantToken("test-secret", "2026-11-12", "203.0.113.7"), before)
	require.Equal(t, wantToken("test-secret", "2026-11-13", "203.0.113.7"), after)

	// The day is the UTC one: 00:30 on the 13th at UTC+2 is still the 12th in UTC.
	c.t = time.Date(2026, 11, 13, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	require.Equal(t, before, tokenOf(t, a, buf, fromIP("203.0.113.7")...))
	// 23:30 on the 12th at UTC-2 is the 13th in UTC.
	c.t = time.Date(2026, 11, 12, 23, 30, 0, 0, time.FixedZone("UTC-2", -2*60*60))
	require.Equal(t, after, tokenOf(t, a, buf, fromIP("203.0.113.7")...))
}

func TestCallerToken_DependsOnTheSecret(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	a, abuf := tokenAPI(t, "secret-one", c.now, nil)
	b, bbuf := tokenAPI(t, "secret-two", c.now, nil)
	one := tokenOf(t, a, abuf, fromIP("203.0.113.7")...)
	two := tokenOf(t, b, bbuf, fromIP("203.0.113.7")...)
	require.NotEqual(t, one, two)
	require.Equal(t, wantToken("secret-one", "2026-11-12", "203.0.113.7"), one)
	require.Equal(t, wantToken("secret-two", "2026-11-12", "203.0.113.7"), two)
}

func TestCallerToken_EmptySecretIsRandomPerProcess(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	a, abuf := tokenAPI(t, "", c.now, nil)
	b, bbuf := tokenAPI(t, "", c.now, nil)
	set, sbuf := tokenAPI(t, "test-secret", c.now, nil)

	first := tokenOf(t, a, abuf, fromIP("203.0.113.7")...)
	require.Equal(t, first, tokenOf(t, a, abuf, fromIP("203.0.113.7")...), "stable within the process and the day")
	require.NotEqual(t, first, tokenOf(t, a, abuf, fromIP("203.0.113.8")...), "another caller, another token")
	require.NotEqual(t, first, tokenOf(t, b, bbuf, fromIP("203.0.113.7")...), "another process, another random secret")
	require.NotEqual(t, first, tokenOf(t, set, sbuf, fromIP("203.0.113.7")...), "a set secret gives other tokens")
	require.NotEqual(t, wantToken("", "2026-11-12", "203.0.113.7"), first, "the empty string is never the secret")

	c.t = time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	require.NotEqual(t, first, tokenOf(t, a, abuf, fromIP("203.0.113.7")...), "the random secret still rotates per UTC day")
}

// Fixed secret, day and caller key give fixed tokens, so no refactor changes them silently. The
// values were computed outside Go (openssl dgst -mac HMAC).
func TestCallerToken_KnownVector(t *testing.T) {
	const secret = "known-vector-secret"
	c := &clock{t: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	a, buf := tokenAPI(t, secret, c.now, testKeys(t))
	ip := fromIP("203.0.113.7")
	keyed := append(fromIP("203.0.113.7"), "X-API-Key", keyAlpha)
	ipv6 := fromIP("2001:db8:1:2::abcd")
	for _, tc := range []struct {
		day, ipTok, keyTok, ipv6Tok string
		at                          time.Time
	}{
		{"2026-11-12", "add486f81360e945", "3413ade0852786fd", "3b3807824543b5ed", time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)},
		{"2026-11-13", "981285a1e624eb93", "498421d67d470b46", "ad57e44fd2a27d8f", time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)},
	} {
		c.t = tc.at
		require.Equal(t, tc.ipTok, tokenOf(t, a, buf, ip...), tc.day)
		require.Equal(t, tc.keyTok, tokenOf(t, a, buf, keyed...), tc.day)
		require.Equal(t, tc.ipv6Tok, tokenOf(t, a, buf, ipv6...), tc.day)
		require.Equal(t, tc.ipTok, wantToken(secret, tc.day, "203.0.113.7"))
		require.Equal(t, tc.keyTok, wantToken(secret, tc.day, "key:alpha"))
		require.Equal(t, tc.ipv6Tok, wantToken(secret, tc.day, "2001:db8:1:2::/64"))
	}
}

// Requests on both sides of midnight at once: every token is the right one of the two days.
// Run under -race, it also proves the cached day key is safe for concurrent use.
func TestCallerToken_ConcurrentDayChangeIsRaceFree(t *testing.T) {
	day1 := time.Date(2026, 11, 12, 23, 59, 59, 0, time.UTC)
	day2 := day1.Add(2 * time.Second)
	var calls atomic.Int64
	now := func() time.Time {
		if calls.Add(1)%2 == 0 {
			return day2
		}
		return day1
	}
	a, buf := tokenAPI(t, "test-secret", now, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				do(a, "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`, fromIP("203.0.113.7")...)
			}
		})
	}
	wg.Wait()

	want := map[string]bool{
		wantToken("test-secret", "2026-11-12", "203.0.113.7"): true,
		wantToken("test-secret", "2026-11-13", "203.0.113.7"): true,
	}
	seen := map[string]int{}
	for _, line := range logLines(t, buf) {
		if line["msg"] != "request" {
			continue
		}
		tok, _ := line["caller"].(string)
		require.True(t, want[tok], "token %q is one of the two days'", tok)
		seen[tok]++
	}
	require.Len(t, seen, 2, "both days were served")
	require.Equal(t, 400, seen[wantToken("test-secret", "2026-11-12", "203.0.113.7")]+seen[wantToken("test-secret", "2026-11-13", "203.0.113.7")])
}

// Two addresses of one IPv6 /64 are one caller token; another /64 is another. Neither address
// reaches the log.
func TestAccessLog_IPv6SlashSixtyFourIsOneCallerToken(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	a, buf := tokenAPI(t, "test-secret", c.now, nil)
	one := tokenOf(t, a, buf, fromIP("2001:db8:1:2::1")...)
	require.Equal(t, one, tokenOf(t, a, buf, fromIP("2001:db8:1:2:ffff:ffff:ffff:ffff")...), "one /64, one token")
	doFrom(a, "[2001:db8:1:2::9]:443", "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)
	require.Equal(t, one, lastToken(t, buf), "the TCP peer is keyed by its /64 too")
	require.NotEqual(t, one, tokenOf(t, a, buf, fromIP("2001:db8:1:3::1")...), "another /64, another token")
	require.Equal(t, wantToken("test-secret", "2026-11-12", "2001:db8:1:2::/64"), one)
	require.NotContains(t, buf.String(), "2001:db8", "no address or prefix reaches the log")
}

// The full log of a run with IPv4, IPv6, IPv4-mapped, peer-only, keyed and wrongly keyed
// callers holds no address, no /64 prefix, no caller key, no API key and not the secret.
func TestAccessLog_CallerTokenNeverCarriesTheAddressOrTheSecret(t *testing.T) {
	const secret = "never-logged-secret-7f3a9c"
	c := &clock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	a, buf := tokenAPI(t, secret, c.now, testKeys(t))
	for _, hdr := range [][]string{
		fromIP("203.0.113.7"),
		fromIP("2001:db8:1:2::1"),
		fromIP("::ffff:198.51.100.4"),
		append(fromIP("203.0.113.9"), "X-API-Key", keyAlpha),
		append(fromIP("203.0.113.10"), "X-API-Key", "wrong-key-0000"),
	} {
		tokenOf(t, a, buf, hdr...)
	}
	doFrom(a, "[2001:db8:5:6::7]:443", "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)
	lastToken(t, buf)
	doFrom(a, "192.0.2.44:5555", "POST", "/v1/tool/get_jumps_between", `{"from_system":"Jita","to_system":"Amarr"}`)
	lastToken(t, buf)

	out := buf.String()
	for _, s := range []string{"203.0.113", "198.51.100", "192.0.2", "2001:db8", "::ffff", "/64", "key:alpha", keyAlpha, "wrong-key-0000", secret} {
		require.NotContains(t, out, s)
	}
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
