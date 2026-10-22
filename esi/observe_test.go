package esi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// events collects CallEvents safely.
type events struct {
	mu  sync.Mutex
	all []CallEvent
}

func (e *events) add(ev CallEvent) {
	e.mu.Lock()
	e.all = append(e.all, ev)
	e.mu.Unlock()
}

func (e *events) list() []CallEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]CallEvent(nil), e.all...)
}

func TestObserverSeesMissHitAndRevalidation(t *testing.T) {
	var fc *fakeClock
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("X-Ratelimit-Group", "market-order")
		w.Header().Set("X-Ratelimit-Limit", "12000/15m")
		w.Header().Set("X-Ratelimit-Remaining", "11990")
		w.Header().Set("X-Ratelimit-Used", "2")
		etagHandler(fc.Now, 300*time.Second, `W/"abc"`, `[]`)(w, r, n)
	})
	var ev events
	c, clock := newTestClient(t, f, Config{Observer: ev.add})
	fc = clock
	ctx := context.Background()

	_, err := c.Get(ctx, "/markets/10000002/orders", map[string][]string{"type_id": {"34"}})
	require.NoError(t, err)
	_, err = c.Get(ctx, "/markets/10000002/orders", map[string][]string{"type_id": {"34"}})
	require.NoError(t, err)
	fc.Advance(301 * time.Second)
	_, err = c.Get(ctx, "/markets/10000002/orders", map[string][]string{"type_id": {"34"}})
	require.NoError(t, err)

	got := ev.list()
	require.Len(t, got, 3)

	require.Equal(t, CacheMiss, got[0].Cache)
	require.True(t, got[0].Sent)
	require.Equal(t, 200, got[0].Status)
	require.Equal(t, "market-order", got[0].Group)
	require.Equal(t, 11990, got[0].RateRemaining)
	require.Equal(t, 12000, got[0].RateLimit)
	require.Equal(t, -1, got[0].ErrorLimitRemain)
	// The route is a template: ids and the query string never reach the monitoring line.
	require.Equal(t, "GET /markets/{}/orders", got[0].Route)

	require.Equal(t, CacheHit, got[1].Cache)
	require.False(t, got[1].Sent)
	require.Equal(t, 0, got[1].Status)

	require.Equal(t, CacheStale, got[2].Cache)
	require.Equal(t, 304, got[2].Status)
}

func TestObserverReportsErrorLimitHeaders(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("X-ESI-Error-Limit-Remain", "97")
		w.Header().Set("X-ESI-Error-Limit-Reset", "40")
		w.WriteHeader(http.StatusNotFound)
	})
	var ev events
	c, _ := newTestClient(t, f, Config{Observer: ev.add})
	r, err := c.Get(context.Background(), "/universe/types/99999999", nil)
	require.NoError(t, err)
	require.Equal(t, 404, r.Status)
	got := ev.list()
	require.Len(t, got, 1)
	require.Equal(t, 97, got[0].ErrorLimitRemain)
	require.Equal(t, 404, got[0].Status)
	require.Equal(t, "GET /universe/types/{}", got[0].Route)
}

func TestAPanickingObserverDoesNotBreakRequests(t *testing.T) {
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) { ok200(w, `{}`, nil) })
	c, _ := newTestClient(t, f, Config{Observer: func(CallEvent) { panic("boom") }})
	r, err := c.Get(context.Background(), "/x", nil)
	require.NoError(t, err)
	require.Equal(t, 200, r.Status)
}

func TestConcurrentCallersOnAColdKeyShareOneRequest(t *testing.T) {
	release := make(chan struct{})
	f := newFakeESI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		<-release
		ok200(w, `{"a":1}`, map[string]string{"ETag": `"e"`, "Date": httpDate(t0), "Expires": httpDate(t0.Add(time.Hour))})
	})
	c, _ := newTestClient(t, f, Config{})

	const callers = 8
	var wg sync.WaitGroup
	bodies := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Get(context.Background(), "/sovereignty/systems", nil)
			if err == nil {
				r.Body[0] = 'X' // a private copy: must not leak to the other callers
				bodies[i] = "ok"
			}
		}()
	}
	require.Eventually(t, func() bool { return f.Count() >= 1 }, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return flightWaiters(c) == callers }, 5*time.Second, time.Millisecond, "every caller joins the flight")
	close(release)
	wg.Wait()

	require.Equal(t, 1, f.Count(), "one upstream request for %d concurrent callers", callers)
	for _, b := range bodies {
		require.Equal(t, "ok", b)
	}
	r, err := c.Get(context.Background(), "/sovereignty/systems", nil)
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(r.Body), "the cache holds the pristine body")
}

func guardedHandler(remaining string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		ok200(w, `{}`, map[string]string{
			"X-Ratelimit-Group":     "g",
			"X-Ratelimit-Limit":     "100/15m",
			"X-Ratelimit-Remaining": remaining,
			"X-Ratelimit-Used":      "2",
		})
	}
}

func TestNegativeMaxWaitRefusesInsteadOfSleeping(t *testing.T) {
	// 6 of 100 tokens left: the default 5 % floor (5) plus the 2-token reservation does
	// not fit. With MaxWait < 0 the client answers busy at once and sends nothing.
	f := newFakeESI(t, guardedHandler("6"))
	var ev events
	c, clock := newTestClient(t, f, Config{MaxWait: -1, Observer: ev.add})
	_, err := c.Get(context.Background(), "/t/1", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.Count())

	_, err = c.Get(context.Background(), "/t/2", nil) // same route template, uncached
	require.ErrorIs(t, err, ErrBackedOff)
	require.Equal(t, 1, f.Count(), "no request was sent")
	require.Zero(t, clock.Slept(), "no sleeping")

	got := ev.list()
	require.Len(t, got, 2)
	require.True(t, got[1].BackedOff)
	require.False(t, got[1].Sent)
	require.NotEmpty(t, got[1].Reason)
}

func TestGuardPercentKeepsMoreBudgetUnspent(t *testing.T) {
	// 40 of 100 tokens left. The default floor (5 %) lets the next request through ...
	f := newFakeESI(t, guardedHandler("40"))
	def, _ := newTestClient(t, f, Config{MaxWait: -1})
	_, err := def.Get(context.Background(), "/t/1", nil)
	require.NoError(t, err)
	_, err = def.Get(context.Background(), "/t/2", nil)
	require.NoError(t, err)

	// ... a 50 % guard does not.
	f2 := newFakeESI(t, guardedHandler("40"))
	guarded, _ := newTestClient(t, f2, Config{MaxWait: -1, GuardPercent: 50})
	_, err = guarded.Get(context.Background(), "/t/1", nil)
	require.NoError(t, err)
	_, err = guarded.Get(context.Background(), "/t/2", nil)
	require.ErrorIs(t, err, ErrBackedOff)
	require.Equal(t, 1, f2.Count())
}
