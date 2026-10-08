package esi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUserAgent(t *testing.T) {
	require.Equal(t, "EVE-Cyno/1.2.3 (+https://eve-cyno.dev)", UserAgent("1.2.3", ""))
	require.Equal(t, "EVE-Cyno/1.2.3 (+https://eve-cyno.dev; ops@example.org)", UserAgent("1.2.3", "ops@example.org"))
	require.Equal(t, "EVE-Cyno/dev (+https://eve-cyno.dev; discord:cyno)", UserAgent("", "discord:cyno"))

	// Header-unsafe input cannot break out of the comment or the header.
	require.Equal(t, "EVE-Cyno/1.0 (+https://eve-cyno.dev; a b evil)", UserAgent("1.0 !", "a\r\nb (evil)"))
	require.NotContains(t, UserAgent("v", "x\nInjected: 1"), "\n")
}

func TestDefaultUserAgentCarriesNoPersonalContact(t *testing.T) {
	c, err := New(Config{})
	require.NoError(t, err)
	require.Equal(t, "EVE-Cyno/dev (+https://eve-cyno.dev)", c.UserAgent())
	require.NotContains(t, c.UserAgent(), "@")
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{"ESI_CONTACT": " ops@example.org ", "ESI_COMPATIBILITY_DATE": "2026-08-18"}
	cfg := FromEnv(func(k string) string { return env[k] })
	require.Equal(t, "ops@example.org", cfg.Contact)
	require.Equal(t, "2026-08-18", cfg.CompatibilityDate)

	cfg.Version = "0.10.3" // set by the caller, not read from the environment
	c, err := New(cfg)
	require.NoError(t, err)
	require.Equal(t, "EVE-Cyno/0.10.3 (+https://eve-cyno.dev; ops@example.org)", c.UserAgent())
	require.Equal(t, "2026-08-18", c.CompatibilityDate())

	empty := FromEnv(func(string) string { return "" })
	require.Equal(t, Config{}, empty)
}

func TestNewValidatesConfig(t *testing.T) {
	for name, cfg := range map[string]Config{
		"relative base":    {BaseURL: "esi.example.org"},
		"ftp base":         {BaseURL: "ftp://esi.example.org"},
		"base with query":  {BaseURL: "https://esi.example.org?x=1"},
		"not a date":       {CompatibilityDate: "latest"},
		"non canonical":    {CompatibilityDate: "2025-1-5"},
		"future date":      {CompatibilityDate: "2999-01-01"},
		"impossible month": {CompatibilityDate: "2025-13-01"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(cfg)
			require.Error(t, err)
		})
	}
	c, err := New(Config{BaseURL: "https://esi.example.org/"})
	require.NoError(t, err)
	require.Equal(t, "https://esi.example.org", c.baseURL)
	require.Equal(t, DefaultCompatibilityDate, c.CompatibilityDate())
}

func TestEveryRequestCarriesUserAgentAndCompatibilityDate(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch {
		case r.Header.Get("If-None-Match") != "":
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Date", httpDate(t0.Add(301*time.Second)))
			w.Header().Set("Expires", httpDate(t0.Add(601*time.Second)))
			w.WriteHeader(http.StatusNotModified)
		case r.URL.Path == "/paged":
			ok200(w, `[1]`, map[string]string{"X-Pages": "2"})
		default:
			ok200(w, `{}`, map[string]string{"ETag": `"v1"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(300 * time.Second))})
		}
	})
	c, fc := newTestClient(t, f, Config{Version: "9.9", Contact: "ops@example.org", CompatibilityDate: "2026-08-18"})
	ctx := context.Background()

	_, err := c.Get(ctx, "/universe/types/587", nil) // plain GET
	require.NoError(t, err)
	fc.Advance(301 * time.Second)
	_, err = c.Get(ctx, "/universe/types/587", nil) // revalidation (If-None-Match)
	require.NoError(t, err)
	_, err = c.Do(ctx, Request{Method: "POST", Path: "/universe/ids", Body: []byte(`["Rifter"]`)}) // POST
	require.NoError(t, err)
	_, err = c.GetAllPages(ctx, Request{Path: "/paged"}, PageOptions{}) // pagination
	require.NoError(t, err)
	_, err = c.Do(ctx, Request{Path: "/x", CompatibilityDate: "2025-12-16"}) // per-request override
	require.NoError(t, err)

	reqs := f.Requests()
	require.GreaterOrEqual(t, len(reqs), 6)
	for _, r := range reqs {
		require.Equal(t, "EVE-Cyno/9.9 (+https://eve-cyno.dev; ops@example.org)", r.Header.Get("User-Agent"), "%s %s", r.Method, r.Path)
		want := "2026-08-18"
		if r.Path == "/x" {
			want = "2025-12-16"
		}
		require.Equal(t, want, r.Header.Get("X-Compatibility-Date"), "%s %s", r.Method, r.Path)
		require.Equal(t, "application/json", r.Header.Get("Accept"))
	}
	require.Equal(t, `"v1"`, reqs[1].Header.Get("If-None-Match"))
	require.Equal(t, "application/json", reqs[2].Header.Get("Content-Type"))
	require.Equal(t, `["Rifter"]`, reqs[2].Body)
}

func TestRequestValidation(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `{}`, nil) })
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()

	for name, req := range map[string]Request{
		"no leading slash":  {Path: "markets"},
		"query in path":     {Path: "/markets?page=1"},
		"fragment":          {Path: "/markets#x"},
		"managed UA":        {Path: "/x", Header: http.Header{"User-Agent": {"evil"}}},
		"managed compat":    {Path: "/x", Header: http.Header{"x-compatibility-date": {"2020-01-01"}}},
		"managed auth":      {Path: "/x", Header: http.Header{"Authorization": {"Bearer x"}}},
		"bad override date": {Path: "/x", CompatibilityDate: "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Do(ctx, req)
			require.Error(t, err)
		})
	}
	require.Zero(t, f.Count(), "invalid requests must not reach the server")
}

func TestQueryAndLanguageHeaderReachTheServer(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `{}`, nil) })
	c, _ := newTestClient(t, f, Config{})

	_, err := c.Do(context.Background(), Request{
		Path:   "/markets/10000002/orders",
		Query:  url.Values{"type_id": {"34"}, "order_type": {"all"}},
		Header: http.Header{"Accept-Language": {"en"}},
	})
	require.NoError(t, err)
	r := f.Requests()[0]
	require.Equal(t, "/markets/10000002/orders", r.Path)
	require.Equal(t, "order_type=all&type_id=34", r.Query) // sorted, deterministic
	require.Equal(t, "en", r.Header.Get("Accept-Language"))
}

// ---- ETag / Expires cache ----------------------------------------------------------

// etagHandler serves body with ETag/Expires relative to the fake clock `now`, and answers 304
// to a matching If-None-Match.
func etagHandler(now func() time.Time, ttl time.Duration, etag, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Date", httpDate(now()))
		w.Header().Set("Expires", httpDate(now().Add(ttl)))
		w.Header().Set("Last-Modified", httpDate(t0.Add(-time.Hour)))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		ok200(w, body, nil)
	}
}

func TestCacheServesFreshThenRevalidatesWith304(t *testing.T) {
	var fc *fakeClock
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		etagHandler(fc.Now, 300*time.Second, `W/"abc"`, `{"name":"Rifter"}`)(w, r, n)
	})
	c, clock := newTestClient(t, f, Config{})
	fc = clock
	ctx := context.Background()

	r1, err := c.Get(ctx, "/universe/types/587", nil)
	require.NoError(t, err)
	require.Equal(t, 200, r1.Status)
	require.False(t, r1.FromCache)
	require.Equal(t, `{"name":"Rifter"}`, string(r1.Body))
	require.Equal(t, `W/"abc"`, r1.ETag)
	require.Equal(t, t0.Add(300*time.Second), r1.Expires)

	// Within Expires: served locally, no request ("You should not update before that").
	fc.Advance(299 * time.Second)
	r2, err := c.Get(ctx, "/universe/types/587", nil)
	require.NoError(t, err)
	require.True(t, r2.FromCache)
	require.False(t, r2.Revalidated)
	require.Equal(t, `{"name":"Rifter"}`, string(r2.Body))
	require.Equal(t, 1, f.Count())

	// Past Expires: conditional request; the 304 returns the cached body as a 200.
	fc.Advance(2 * time.Second)
	r3, err := c.Get(ctx, "/universe/types/587", nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count())
	require.Equal(t, `W/"abc"`, f.Requests()[1].Header.Get("If-None-Match"))
	require.Equal(t, 200, r3.Status)
	require.True(t, r3.FromCache)
	require.True(t, r3.Revalidated)
	require.Equal(t, `{"name":"Rifter"}`, string(r3.Body))
	require.Equal(t, t0.Add(601*time.Second), r3.Expires, "the 304's Expires renews the entry")

	// ... and the renewed entry is fresh again.
	_, err = c.Get(ctx, "/universe/types/587", nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count())
}

func TestCacheReplacesBodyWhenETagChanges(t *testing.T) {
	var fc *fakeClock
	version := "v1"
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		etagHandler(fc.Now, 60*time.Second, `"`+version+`"`, `{"v":"`+version+`"}`)(w, r, n)
	})
	c, clock := newTestClient(t, f, Config{})
	fc = clock
	ctx := context.Background()

	r1, _ := c.Get(ctx, "/x", nil)
	require.Equal(t, `{"v":"v1"}`, string(r1.Body))
	version = "v2"
	fc.Advance(61 * time.Second)
	r2, err := c.Get(ctx, "/x", nil)
	require.NoError(t, err)
	require.False(t, r2.FromCache, "a changed ETag is a full 200")
	require.Equal(t, `{"v":"v2"}`, string(r2.Body))
	r3, _ := c.Get(ctx, "/x", nil)
	require.True(t, r3.FromCache)
	require.Equal(t, `{"v":"v2"}`, string(r3.Body))
}

func TestCacheIgnoresClockSkewViaDateHeader(t *testing.T) {
	// ESI's clock is 10 minutes behind ours: Expires-Date is still the 300 s lifetime.
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		skewed := t0.Add(-10 * time.Minute)
		ok200(w, `{}`, map[string]string{"ETag": `"e"`, "Date": httpDate(skewed), "Expires": httpDate(skewed.Add(300 * time.Second))})
	})
	c, fc := newTestClient(t, f, Config{})
	r, err := c.Get(context.Background(), "/x", nil)
	require.NoError(t, err)
	require.Equal(t, t0.Add(300*time.Second), r.Expires)
	fc.Advance(299 * time.Second)
	r, _ = c.Get(context.Background(), "/x", nil)
	require.True(t, r.FromCache)
	require.Equal(t, 1, f.Count())
}

func TestCacheIsolation(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{"auth":"`+r.Header.Get("Authorization")+`","lang":"`+r.Header.Get("Accept-Language")+`","compat":"`+r.Header.Get("X-Compatibility-Date")+`"}`,
			map[string]string{"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
	})
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()
	get := func(req Request) *Response {
		t.Helper()
		r, err := c.Do(ctx, req)
		require.NoError(t, err)
		return r
	}

	// Two characters never share a cached response.
	a := get(Request{Path: "/characters/1/assets", AccessToken: "tok-a", Scope: "character:1"})
	b := get(Request{Path: "/characters/1/assets", AccessToken: "tok-b", Scope: "character:2"})
	require.Contains(t, string(a.Body), "tok-a")
	require.Contains(t, string(b.Body), "tok-b")
	require.Equal(t, 2, f.Count())
	require.True(t, get(Request{Path: "/characters/1/assets", AccessToken: "tok-a", Scope: "character:1"}).FromCache)

	// An authenticated request without a Scope is never cached.
	get(Request{Path: "/characters/9/assets", AccessToken: "tok-z"})
	n := f.Count()
	require.False(t, get(Request{Path: "/characters/9/assets", AccessToken: "tok-z"}).FromCache)
	require.Equal(t, n+1, f.Count())

	// ESI varies on language and compatibility date.
	en := get(Request{Path: "/universe/types/1", Header: http.Header{"Accept-Language": {"en"}}})
	de := get(Request{Path: "/universe/types/1", Header: http.Header{"Accept-Language": {"de"}}})
	require.Contains(t, string(en.Body), `"lang":"en"`)
	require.Contains(t, string(de.Body), `"lang":"de"`)
	old := get(Request{Path: "/universe/types/1", Header: http.Header{"Accept-Language": {"en"}}, CompatibilityDate: "2025-11-06"})
	require.False(t, old.FromCache)
	require.Contains(t, string(old.Body), "2025-11-06")
}

func TestPostAndNoStoreAreNotCached(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		h := map[string]string{"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))}
		if r.URL.Path == "/nostore" {
			h["Cache-Control"] = "no-store"
		}
		ok200(w, `{}`, h)
	})
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := c.Do(ctx, Request{Method: "POST", Path: "/universe/names", Body: []byte(`[1]`)})
		require.NoError(t, err)
		_, err = c.Get(ctx, "/nostore", nil)
		require.NoError(t, err)
	}
	require.Equal(t, 4, f.Count())
	require.Zero(t, c.cache.len())
}

func TestCacheCanBeDisabledAndIsBounded(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, strings.Repeat("x", 300), map[string]string{"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
	})
	off, _ := newTestClient(t, f, Config{CacheBytes: -1})
	_, _ = off.Get(context.Background(), "/a", nil)
	_, _ = off.Get(context.Background(), "/a", nil)
	require.Equal(t, 2, f.Count())

	// Each entry costs ~300+key+512 bytes (under the 1 KiB per-entry cap); a 4 KiB cache
	// keeps only the most recent ones.
	small, _ := newTestClient(t, f, Config{CacheBytes: 4096})
	for i := 0; i < 20; i++ {
		_, err := small.Get(context.Background(), "/p"+string(rune('a'+i)), nil)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, small.cache.len(), 4)
	require.Greater(t, small.cache.len(), 0)
}

func TestBodyTooLarge(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, strings.Repeat("y", 2000), nil) })
	c, _ := newTestClient(t, f, Config{MaxBodyBytes: 1000})
	_, err := c.Get(context.Background(), "/big", nil)
	require.ErrorIs(t, err, ErrBodyTooLarge)
}

// ---- typed errors -------------------------------------------------------------------

func TestTypedStatusErrors(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Type not found"}`))
		case "/boom":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream\n  down"))
		default:
			ok200(w, `{"ok":true}`, nil)
		}
	})
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()

	r, err := c.Get(ctx, "/missing", nil)
	require.NoError(t, err, "an HTTP error status is not a Go error")
	require.Equal(t, 404, r.Status)
	var se *StatusError
	require.ErrorAs(t, r.Err(), &se)
	require.Equal(t, 404, se.Status)
	require.Equal(t, `{"error":"Type not found"}`, se.Body)
	require.Contains(t, se.Error(), "HTTP 404")

	require.ErrorAs(t, c.GetJSON(ctx, "/boom", nil, new(any)), &se)
	require.Equal(t, 502, se.Status)
	require.Equal(t, "upstream down", se.Body)

	var out struct{ OK bool }
	require.NoError(t, c.GetJSON(ctx, "/fine", nil, &out))
	require.True(t, out.OK)
	require.Error(t, c.GetJSON(ctx, "/fine", nil, new(int)), "a JSON shape mismatch is an error")
}

func TestCompatibilityDates(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		require.Equal(t, "/meta/compatibility-dates", r.URL.Path)
		ok200(w, `{"compatibility_dates":["2026-08-18","2020-01-01"]}`, nil)
	})
	c, _ := newTestClient(t, f, Config{})
	dates, err := c.CompatibilityDates(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"2026-08-18", "2020-01-01"}, dates)
}

// ---- concurrency --------------------------------------------------------------------

func TestConcurrentUseIsSafeAndBounded(t *testing.T) {
	var mu sync.Mutex
	inflight, peak := 0, 0
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		mu.Unlock()
		time.Sleep(3 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		ok200(w, `{}`, map[string]string{
			"ETag": `"e"`, "X-Ratelimit-Group": "universe", "X-Ratelimit-Limit": "100000/15m",
			"X-Ratelimit-Remaining": "99000", "X-Ratelimit-Used": "2",
		})
	})
	cfg := Config{BaseURL: f.srv.URL, MaxConcurrent: 3}
	c, err := New(cfg) // real clock and real sleeps: this test is about -race and the cap
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for g := 0; g < 40; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				// A mix of repeated (cacheable) and distinct URLs, from two scopes.
				path := "/universe/types/" + string(rune('0'+(g+i)%10))
				_, err := c.Do(context.Background(), Request{Path: path, AccessToken: "t", Scope: "character:" + string(rune('a'+g%2))})
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.LessOrEqual(t, peak, 3, "MaxConcurrent must bound requests in flight")
	require.Greater(t, peak, 1)
}

func TestMinIntervalSpacesRequestStarts(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `{}`, nil) })
	c, fc := newTestClient(t, f, Config{MinInterval: 250 * time.Millisecond})
	for i := 0; i < 5; i++ {
		_, err := c.Get(context.Background(), "/x"+string(rune('a'+i)), nil)
		require.NoError(t, err)
	}
	// The first request starts at once; each following one waits out the 250 ms spacing.
	require.Equal(t, time.Second, fc.Slept())
}

func TestContextCancelWhileWaitingIsABackoffError(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `{}`, nil) })
	c, _ := newTestClient(t, f, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Get(ctx, "/x", nil)
	var be *BackoffError
	require.ErrorAs(t, err, &be)
	require.ErrorIs(t, err, ErrBackedOff)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, f.Count())
	require.False(t, errors.Is(err, ErrRateLimited))
}

// Config.Now is the clock seam for the tests of callers in other modules (ingest/intel): it
// drives cache freshness and the Expires adjustment without reaching into unexported fields.
func TestConfigNowOverridesTheClock(t *testing.T) {
	fc := newFakeClock()
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{"ETag": `"e"`, "Date": httpDate(fc.Now()), "Expires": httpDate(fc.Now().Add(time.Hour))})
	})
	c, err := New(Config{BaseURL: f.srv.URL, Now: fc.Now})
	require.NoError(t, err)
	ctx := context.Background()

	r, err := c.Get(ctx, "/x", nil)
	require.NoError(t, err)
	require.Equal(t, t0.Add(time.Hour), r.Expires, "Expires is adjusted on the configured clock")

	fc.Advance(59 * time.Minute)
	r, err = c.Get(ctx, "/x", nil)
	require.NoError(t, err)
	require.True(t, r.FromCache, "fresh on the configured clock: no request")
	require.Equal(t, 1, f.Count())

	fc.Advance(2 * time.Minute)
	_, err = c.Get(ctx, "/x", nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count(), "expired on the configured clock: a new request")

	// The date validation uses the same clock: t0 is 2026-10-06, so a later date is "in the future".
	_, err = New(Config{Now: fc.Now, CompatibilityDate: "2026-10-07"})
	require.Error(t, err)
}

func TestNewZeroConfig(t *testing.T) {
	// core/tools relies on this: an empty Config is valid, so its fallback can ignore the error.
	c, err := New(Config{})
	require.NoError(t, err)
	require.Equal(t, DefaultBaseURL, c.baseURL)
	require.Equal(t, DefaultCompatibilityDate, c.CompatibilityDate())
}
