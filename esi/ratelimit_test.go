package esi

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRateLimitHeaders(t *testing.T) {
	// Header names and the "150/15m" format are quoted from
	// https://developers.eveonline.com/docs/services/esi/rate-limiting/ (verified live 2026-10-06).
	h := http.Header{}
	h.Set("X-Ratelimit-Group", "market-order")
	h.Set("X-Ratelimit-Limit", "12000/15m")
	h.Set("X-Ratelimit-Remaining", "11991")
	h.Set("X-Ratelimit-Used", "2")
	require.Equal(t, RateLimit{Group: "market-order", Limit: 12000, Window: 15 * time.Minute, Remaining: 11991, Used: 2}, parseRateLimit(h))

	require.Equal(t, RateLimit{Remaining: -1, Used: -1}, parseRateLimit(http.Header{}), "absent headers: route not bucketed")

	for in, want := range map[string]time.Duration{"150/15m": 15 * time.Minute, "10/1h": time.Hour, "5/30s": 30 * time.Second, "5/1d": 24 * time.Hour} {
		_, w := parseLimit(in)
		require.Equal(t, want, w, in)
	}
	for _, bad := range []string{"", "150", "150/", "150/15", "150/15x", "x/15m", "0/15m", "-1/15m", "150/0m"} {
		n, w := parseLimit(bad)
		require.Zero(t, n, bad)
		require.Zero(t, w, bad)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := t0
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7", 7 * time.Second, true},
		{" 0 ", 0, true},
		{httpDate(now.Add(90 * time.Second)), 90 * time.Second, true},
		{httpDate(now.Add(-time.Minute)), 0, true},
		{"", 0, false},
		{"soon", 0, false},
		{"-5", 0, false},
	} {
		h := http.Header{}
		if tc.in != "" {
			h.Set("Retry-After", tc.in)
		}
		got, ok := parseRetryAfter(h, now)
		require.Equal(t, tc.ok, ok, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
}

func TestRouteKeyGroupsSiblingRoutes(t *testing.T) {
	require.Equal(t, "GET /universe/types/{}", routeKey("GET", "/universe/types/587"))
	require.Equal(t, routeKey("GET", "/universe/types/587"), routeKey("GET", "/universe/types/34"))
	require.Equal(t, "GET /killmails/{}/{}", routeKey("GET", "/killmails/138828372/cbcc2035541422d86fd8908d1b0d2e2f7cf8c6b0"))
	require.Equal(t, "GET /characters/{}/assets", routeKey("GET", "/characters/95465499/assets"))
	require.Equal(t, "GET /meta/compatibility-dates", routeKey("GET", "/meta/compatibility-dates"))
	require.NotEqual(t, routeKey("GET", "/alliances"), routeKey("POST", "/alliances"))
	require.Equal(t, "GET /status", routeKey("GET", "/status/")) // "dead" is hex but short
	require.Equal(t, "GET /x/dead", routeKey("GET", "/x/dead"))
}

// bucketAt builds a bucket that has seen one response at `at`.
func bucketAt(at time.Time, limit int, window time.Duration, remaining, used int) *bucket {
	b := &bucket{}
	b.settle(at, false, RateLimit{Group: "g", Limit: limit, Window: window, Remaining: remaining, Used: used})
	return b
}

func TestBucketReservesUntilTheFloor(t *testing.T) {
	// limit 100 -> floor 5; a request reserves 2. remaining 11: 11-2 >= 5 ok; then 9-... etc.
	b := bucketAt(t0, 100, 15*time.Minute, 11, 2)
	require.Equal(t, 5, b.floor())

	ok, _, _ := b.reserve(t0) // avail 11 -> 9 left >= 5
	require.True(t, ok)
	ok, _, _ = b.reserve(t0) // avail 9 (2 in flight) -> 7 left >= 5
	require.True(t, ok)
	ok, wait, certain := b.reserve(t0) // avail 7 -> would leave 5? 7-2=5 >= floor: still ok
	require.True(t, ok)
	require.Zero(t, wait)
	require.True(t, certain)

	// avail is now 5 with 3 requests in flight: 5-2 < floor. Our own first spend (2 tokens,
	// made at t0) returns at t0+15m and covers the shortfall, so that is the computed wait.
	ok, wait, certain = b.reserve(t0)
	require.False(t, ok)
	require.True(t, certain)
	require.Equal(t, 15*time.Minute, wait)

	// The three in-flight requests answer; the server now says 3 remain.
	for i := 0; i < 3; i++ {
		b.settle(t0.Add(time.Second), true, RateLimit{Group: "g", Limit: 100, Window: 15 * time.Minute, Remaining: 5 - i, Used: 2})
	}
	require.Zero(t, b.inflight)
	ok, wait, certain = b.reserve(t0.Add(time.Second))
	require.False(t, ok)
	require.True(t, certain)
	require.Equal(t, 15*time.Minute, wait, "need 4 tokens: the t0 spend and the first t0+1s spend, back at t0+15m+1s")
}

func TestBucketPollsForInFlightResponsesWhenSpendsCannotCoverIt(t *testing.T) {
	b := &bucket{}
	b.settle(t0, false, RateLimit{Group: "g", Limit: 100, Window: 15 * time.Minute, Remaining: 10, Used: 0}) // spent by other clients, not by us
	for i := 0; i < 2; i++ {
		ok, _, _ := b.reserve(t0)
		require.True(t, ok)
	}
	ok, wait, certain := b.reserve(t0) // avail 6: 6-2 < floor 5, no own spend to wait for, 2 in flight
	require.False(t, ok)
	require.False(t, certain, "in-flight responses will refresh remaining; poll for them")
	require.Equal(t, pollInterval, wait)

	b.settle(t0, true, RateLimit{Group: "g", Limit: 100, Window: 15 * time.Minute, Remaining: 5, Used: 0})
	b.settle(t0, true, RateLimit{Group: "g", Limit: 100, Window: 15 * time.Minute, Remaining: 4, Used: 0})
	ok, wait, certain = b.reserve(t0.Add(time.Second))
	require.False(t, ok)
	require.True(t, certain)
	require.Equal(t, 15*time.Minute-time.Second, wait, "nothing in flight, no spends: everything is back one window after the snapshot")
}

func TestBucketReleasesSpendsAfterTheWindow(t *testing.T) {
	b := bucketAt(t0, 100, 15*time.Minute, 6, 2)
	b.settle(t0.Add(time.Minute), false, RateLimit{Limit: 100, Window: 15 * time.Minute, Remaining: 4, Used: 2})

	ok, wait, certain := b.reserve(t0.Add(2 * time.Minute)) // 4-2 = 2 < floor 5
	require.False(t, ok)
	require.True(t, certain)
	// need = floor+cost-avail = 5+2-4 = 3 tokens: the first spend (2) is not enough, the second brings it to 4.
	require.Equal(t, 14*time.Minute, wait, "wait until t0+16m, when both spends are back")

	ok, _, _ = b.reserve(t0.Add(16 * time.Minute)) // both released: avail 4+2+2 = 8 -> ok
	require.True(t, ok)
}

func TestBucketRefillsAfterAWholeIdleWindow(t *testing.T) {
	b := bucketAt(t0, 100, 15*time.Minute, 0, 2) // server says empty
	ok, _, _ := b.reserve(t0.Add(time.Minute))
	require.False(t, ok)
	ok, _, _ = b.reserve(t0.Add(15*time.Minute + time.Second)) // a full window without any response
	require.True(t, ok)
}

func TestBucketUnknownGroupSendsAndLearns(t *testing.T) {
	b := &bucket{} // created for a group but no remaining header seen yet
	ok, _, _ := b.reserve(t0)
	require.True(t, ok)
	ok, _, _ = b.reserve(t0)
	require.True(t, ok, "no data: never block")
}

func TestBucketBlockAdmitsOneRequestAfterRetryAfter(t *testing.T) {
	b := bucketAt(t0, 100, 15*time.Minute, 0, 2)
	b.block(t0, 30*time.Second)

	ok, wait, certain := b.reserve(t0.Add(10 * time.Second))
	require.False(t, ok)
	require.True(t, certain)
	require.Equal(t, 20*time.Second, wait)

	ok, _, _ = b.reserve(t0.Add(30 * time.Second))
	require.True(t, ok, "one request fits once Retry-After has passed")
	ok, _, certain = b.reserve(t0.Add(30 * time.Second))
	require.False(t, ok, "but only one, until its response restores the real counts")
	require.False(t, certain)
}

func TestBucketCancelReturnsTheReservation(t *testing.T) {
	b := bucketAt(t0, 100, 15*time.Minute, 7, 2)
	ok, _, _ := b.reserve(t0)
	require.True(t, ok)
	require.Equal(t, reserveCost, b.inflight)
	b.cancel()
	require.Zero(t, b.inflight)
	b.cancel()
	require.Zero(t, b.inflight, "never negative")
}

// ---- through the client ---------------------------------------------------------------

// bucketedHandler answers every request with group g, limit 100/15m and a remaining count
// that starts at `start` and drops by `used` per request.
func bucketedHandler(start, used int) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{
			"X-Ratelimit-Group":     "g",
			"X-Ratelimit-Limit":     "100/15m",
			"X-Ratelimit-Remaining": strconv.Itoa(max(start-used*n, 0)),
			"X-Ratelimit-Used":      strconv.Itoa(used),
		})
	}
}

func TestClientNeverOverdrawsAGroupBucket(t *testing.T) {
	f := newFakeESI(t, bucketedHandler(12, 2)) // remaining after request n: 12-2n
	c, fc := newTestClient(t, f, Config{})
	ctx := context.Background()

	// floor is 5: sends while remaining-2 >= 5, i.e. requests 1..3 (remaining 10, 8, 6).
	for i := 0; i < 3; i++ {
		r, err := c.Get(ctx, "/universe/types/"+strconv.Itoa(100+i), nil) // sibling routes share the group
		require.NoError(t, err)
		require.Equal(t, 200, r.Status)
		require.Equal(t, "g", r.Rate.Group)
		require.Equal(t, 12-2*(i+1), r.Rate.Remaining)
	}
	require.Equal(t, 3, f.Count())

	// The fourth would leave 4 < floor: refused without sending, as the next release is 15 min away.
	_, err := c.Get(ctx, "/universe/types/200", nil)
	var be *BackoffError
	require.ErrorAs(t, err, &be)
	require.Contains(t, be.Reason, "bucket")
	require.WithinDuration(t, t0.Add(15*time.Minute), be.Until, time.Second)
	require.Equal(t, 3, f.Count(), "an exhausted bucket must not be probed")

	// Once the window has passed the spends are back and requests flow again.
	fc.Advance(15*time.Minute + time.Second)
	_, err = c.Get(ctx, "/universe/types/200", nil)
	require.NoError(t, err)
	require.Equal(t, 4, f.Count())
}

func TestClientWaitsForABucketThatReleasesWithinMaxWait(t *testing.T) {
	// A bucket with a one-minute window whose spends come back inside MaxWait=2m.
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{
			"X-Ratelimit-Group": "short", "X-Ratelimit-Limit": "20/1m",
			"X-Ratelimit-Remaining": strconv.Itoa(max(8-2*n, 0)), "X-Ratelimit-Used": "2", // floor is 1
		})
	})
	c, fc := newTestClient(t, f, Config{MaxWait: 2 * time.Minute})
	for i := 0; i < 4; i++ { // remaining: 6, 4, 2, 0 -> the fifth must wait
		_, err := c.Get(context.Background(), "/x/"+strconv.Itoa(i), nil)
		require.NoError(t, err)
	}
	slept := fc.Slept()
	_, err := c.Get(context.Background(), "/x/9", nil)
	require.NoError(t, err)
	require.Greater(t, fc.Slept()-slept, 0*time.Second, "the client slept instead of sending into an empty bucket")
	require.GreaterOrEqual(t, fc.Now().Sub(t0), time.Minute, "and only sent after the first spends had come back")
	require.Equal(t, 5, f.Count())
}

func TestBucketsAreScopedPerCharacter(t *testing.T) {
	f := newFakeESI(t, bucketedHandler(12, 2)) // each (group, scope) bucket drains independently
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := c.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "a", Scope: "character:1"})
		require.NoError(t, err)
	}
	_, err := c.Do(ctx, Request{Path: "/characters/1/assets", AccessToken: "a", Scope: "character:1"})
	require.ErrorIs(t, err, ErrBackedOff)

	// Another character, and the unauthenticated IP-level bucket, are untouched.
	_, err = c.Do(ctx, Request{Path: "/characters/2/assets", AccessToken: "b", Scope: "character:2"})
	require.NoError(t, err)
	_, err = c.Get(ctx, "/characters/2/assets", nil)
	require.NoError(t, err)
}

func TestRetryAfterOn429IsHonouredAndRetried(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("X-Ratelimit-Group", "g")
			w.Header().Set("X-Ratelimit-Limit", "100/15m")
			w.Header().Set("X-Ratelimit-Remaining", "0")
			w.Header().Set("Retry-After", "4")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ok200(w, `{"ok":true}`, map[string]string{"X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "100/15m", "X-Ratelimit-Remaining": "90", "X-Ratelimit-Used": "2"})
	})
	c, fc := newTestClient(t, f, Config{})

	r, err := c.Get(context.Background(), "/universe/types/1", nil)
	require.NoError(t, err)
	require.Equal(t, 200, r.Status)
	require.Equal(t, 2, f.Count())
	require.GreaterOrEqual(t, fc.Now().Sub(t0), 4*time.Second, "waited Retry-After before the retry")
}

func TestLongRetryAfterReturnsThe429AndBlocksFurtherRequests(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("X-Ratelimit-Group", "g")
			w.Header().Set("X-Ratelimit-Limit", "100/15m")
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		ok200(w, `{}`, map[string]string{"X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "100/15m", "X-Ratelimit-Remaining": "90", "X-Ratelimit-Used": "2"})
	})
	c, fc := newTestClient(t, f, Config{}) // MaxWait 10 s < 120 s
	ctx := context.Background()

	r, err := c.Get(ctx, "/universe/types/1", nil)
	require.NoError(t, err, "a 429 is a response, not a Go error")
	require.Equal(t, 429, r.Status)
	require.Equal(t, 120*time.Second, r.RetryAfter)
	var rl *RateLimitError
	require.ErrorAs(t, r.Err(), &rl)
	require.ErrorIs(t, r.Err(), ErrRateLimited)
	require.Equal(t, "g", rl.Group)
	require.Equal(t, 120*time.Second, rl.RetryAfter)
	require.Equal(t, 1, f.Count(), "no retry: the wait does not fit MaxWait")

	// Sibling routes of the group are held back without being sent ...
	_, err = c.Get(ctx, "/universe/types/2", nil)
	var be *BackoffError
	require.ErrorAs(t, err, &be)
	require.WithinDuration(t, t0.Add(120*time.Second), be.Until, time.Second)
	require.Equal(t, 1, f.Count())

	// ... until Retry-After has passed.
	fc.Advance(121 * time.Second)
	_, err = c.Get(ctx, "/universe/types/2", nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.Count())
}

func TestRetryAfterWithoutAGroupBlocksThatRouteOnly(t *testing.T) {
	// Some routes sit behind a limiter deep in EVE Server code: 429 without rate-limit headers.
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/hot" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ok200(w, `{}`, nil)
	})
	c, _ := newTestClient(t, f, Config{})
	ctx := context.Background()
	r, err := c.Get(ctx, "/hot", nil)
	require.NoError(t, err)
	require.Equal(t, 429, r.Status)
	_, err = c.Get(ctx, "/hot", nil)
	require.ErrorIs(t, err, ErrBackedOff)
	_, err = c.Get(ctx, "/cold", nil)
	require.NoError(t, err)
}

func TestErrorLimit420PausesEveryRoute(t *testing.T) {
	// Legacy error limit (best practices): 420 on all routes, X-ESI-Error-Limit-Reset seconds left.
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("X-ESI-Error-Limit-Remain", "0")
			w.Header().Set("X-ESI-Error-Limit-Reset", "30")
			w.WriteHeader(420)
			return
		}
		ok200(w, `{}`, nil)
	})
	c, fc := newTestClient(t, f, Config{})
	ctx := context.Background()

	r, err := c.Get(ctx, "/a", nil)
	require.NoError(t, err)
	require.Equal(t, 420, r.Status)
	require.Equal(t, 30*time.Second, r.RetryAfter)
	var rl *RateLimitError
	require.ErrorAs(t, r.Err(), &rl)
	require.Equal(t, 1, f.Count())

	_, err = c.Get(ctx, "/b/other", nil)
	var be *BackoffError
	require.ErrorAs(t, err, &be)
	require.Contains(t, be.Reason, "420")
	require.Equal(t, 1, f.Count())

	fc.Advance(32 * time.Second)
	_, err = c.Get(ctx, "/b/other", nil)
	require.NoError(t, err)
}

func TestLegacyErrorLimitBacksOffBeforeItReachesZero(t *testing.T) {
	remain := "50"
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{"X-ESI-Error-Limit-Remain": remain, "X-ESI-Error-Limit-Reset": "25"})
	})
	c, fc := newTestClient(t, f, Config{}) // floor 20, MaxWait 10 s
	ctx := context.Background()

	_, err := c.Get(ctx, "/a", nil)
	require.NoError(t, err)
	_, err = c.Get(ctx, "/b", nil)
	require.NoError(t, err, "remain 50 is above the floor")

	remain = "20" // at the floor: stop until the window resets
	_, err = c.Get(ctx, "/c", nil)
	require.NoError(t, err)
	_, err = c.Get(ctx, "/d", nil)
	var be *BackoffError
	require.ErrorAs(t, err, &be)
	require.Contains(t, be.Reason, "X-ESI-Error-Limit-Remain=20")
	require.WithinDuration(t, t0.Add(26*time.Second), be.Until, time.Second, "Reset (25 s) plus a 1 s margin")
	require.Equal(t, 3, f.Count())

	remain = "100"
	fc.Advance(27 * time.Second)
	_, err = c.Get(ctx, "/d", nil)
	require.NoError(t, err)
}

func TestLegacyErrorLimitShortWaitIsSleptNotFailed(t *testing.T) {
	remain := "10"
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{"X-ESI-Error-Limit-Remain": remain, "X-ESI-Error-Limit-Reset": "4"})
	})
	c, fc := newTestClient(t, f, Config{})
	_, err := c.Get(context.Background(), "/a", nil)
	require.NoError(t, err)
	remain = "100"
	_, err = c.Get(context.Background(), "/b", nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, fc.Now().Sub(t0), 5*time.Second, "slept through the 4 s reset (+1 s margin) instead of failing")
	require.Equal(t, 2, f.Count())
}

func TestRetriesAreBounded(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, _ := newTestClient(t, f, Config{MaxRetries: 2})
	r, err := c.Get(context.Background(), "/x", nil)
	require.NoError(t, err)
	require.Equal(t, 429, r.Status)
	require.Equal(t, 3, f.Count(), "one try plus MaxRetries")

	f2 := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(http.StatusTooManyRequests) })
	c2, _ := newTestClient(t, f2, Config{MaxRetries: -1})
	_, err = c2.Get(context.Background(), "/x", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f2.Count(), "negative MaxRetries disables retrying")
}

func TestBucketIsRaceFree(t *testing.T) {
	b := bucketAt(t0, 1000, 15*time.Minute, 900, 2)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				now := t0.Add(time.Duration(i) * time.Second)
				if ok, _, _ := b.reserve(now); ok {
					b.settle(now, true, RateLimit{Group: "g", Limit: 1000, Window: 15 * time.Minute, Remaining: 900 - i, Used: 2})
				}
			}
		}()
	}
	wg.Wait()
	require.Zero(t, b.inflight)
}
