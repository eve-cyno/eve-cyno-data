package dataapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/hubprices"
)

// A client that asks again exactly at the published nextBuildAt gets the next table, every
// cycle: no table is skipped. The build takes 30.5 s (a fraction, so the stamp's rounding
// matters) and the publish 1.2 s more.
func TestHubRefresher_PollAtNextBuildAtGetsTheNewTable(t *testing.T) {
	const every = 10 * time.Minute
	h, r, ctx := newHubHarness(t, every)
	h.pubCost = 1200 * time.Millisecond
	h.restampCost = 500 * time.Millisecond
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30500*time.Millisecond) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step, step, step}

	var prevNext time.Time
	var prevETag string
	polled := 0
	h.onSleep = func(n int) {
		rec := do(h.api, http.MethodGet, "/v1/datasets/"+hubsName, "")
		require.Equal(t, http.StatusOK, rec.Code)
		etag := rec.Header().Get("ETag")
		var w struct {
			BuiltAt     time.Time `json:"builtAt"`
			NextBuildAt time.Time `json:"nextBuildAt"`
		}
		require.NoError(t, jsonUnmarshal(rec.Body.String(), &w))
		if n > 1 { // table 1 is stamped from its own measured duration (restamped), so every later poll is checked
			// The client came back at the previous table's nextBuildAt: this table (publish n)
			// must be out by then, and the poll gets it, not a 304.
			now := h.clk.now()
			delta := prevNext.Sub(now)
			require.GreaterOrEqual(t, int64(delta), int64(0), "table %d: nextBuildAt %s of the previous table is before the publish at %s", n, prevNext, now)
			h.clk.add(delta)
			got := do(h.api, http.MethodGet, "/v1/datasets/"+hubsName, "", "If-None-Match", prevETag)
			h.clk.add(-delta)
			require.Equal(t, http.StatusOK, got.Code, "table %d: a poll at nextBuildAt gets the new table", n)
			require.NotEqual(t, prevETag, got.Header().Get("ETag"))
			polled++
		}
		prevNext, prevETag = w.NextBuildAt, etag
		if n == 4 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 4)
	require.Equal(t, 3, polled)
}

// A request at or after the table's nextBuildAt, before the next table is out, gets max-age=0
// and no Retry-After (200 and 304 alike); the client waits by its own rule from Date. A request
// before nextBuildAt gets a positive max-age. After the publish the new table is served.
func TestHubRefresher_DueRequestHasMaxAgeZeroAndNoRetryAfter(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) }
	// Build 3 starts 20 s late, so it ends 20 s past table 2's nextBuildAt.
	h.late = func(n int) time.Duration {
		if n == 2 {
			return 20 * time.Second
		}
		return 0
	}
	var etag2 string
	var next2 time.Time
	get := func(hdr ...string) *httptest.ResponseRecorder {
		return do(h.api, http.MethodGet, "/v1/datasets/"+hubsName, "", hdr...)
	}
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		step, step,
		func(c hubCall) (hubprices.Result, time.Duration) {
			before := get()
			require.Equal(t, http.StatusOK, before.Code)
			require.NotEqual(t, "public, max-age=0", before.Header().Get("Cache-Control"), "not due yet")
			require.Empty(t, before.Header().Get("Retry-After"))
			h.clk.add(next2.Sub(h.clk.now())) // at nextBuildAt: due, nothing new published
			for _, hdr := range [][]string{nil, {"If-None-Match", etag2}} {
				rec := get(hdr...)
				want := http.StatusOK
				if hdr != nil {
					want = http.StatusNotModified
				}
				require.Equal(t, want, rec.Code)
				require.Equal(t, "public, max-age=0", rec.Header().Get("Cache-Control"))
				require.Empty(t, rec.Header().Values("Retry-After"))
			}
			return complete(c, c.at.Add(30*time.Second).Sub(h.clk.now()))
		},
	}
	h.onSleep = func(n int) {
		rec := get()
		require.Equal(t, http.StatusOK, rec.Code)
		switch n {
		case 2:
			etag2 = rec.Header().Get("ETag")
			var w struct {
				NextBuildAt time.Time `json:"nextBuildAt"`
			}
			require.NoError(t, jsonUnmarshal(rec.Body.String(), &w))
			next2 = w.NextBuildAt
		case 3:
			require.NotEqual(t, etag2, rec.Header().Get("ETag"), "after the planned end the new table is served")
			require.Empty(t, rec.Header().Get("Retry-After"))
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
}

type wireTable struct {
	BuiltAt     time.Time `json:"builtAt"`
	NextBuildAt time.Time `json:"nextBuildAt"`
}

func (h *hubHarness) served(hdr ...string) (*httptest.ResponseRecorder, wireTable) {
	h.t.Helper()
	rec := do(h.api, http.MethodGet, "/v1/datasets/"+hubsName, "", hdr...)
	var w wireTable
	if rec.Code == http.StatusOK {
		require.NoError(h.t, jsonUnmarshal(rec.Body.String(), &w))
	}
	return rec, w
}

// The first table after a start names the time table 2 will be out, from that build's own
// measured duration (build 30.5 s + one Prepare 1.2 s = 31.7 s), not the bare next tick. The
// restamp's own encode (0.5 s) and second Prepare (1.2 s) run after the measurement: they do
// not move the grid, table 2 is out before table 1's nextBuildAt, and table 2 is stamped from
// the 31.7 s, not from the longer first build.
func TestHubRefresher_FirstTableNamesItsOwnBuildDuration(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.pubCost, h.restampCost = 1200*time.Millisecond, 500*time.Millisecond
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30500*time.Millisecond) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step}
	var w1, w2 wireTable
	var out1, out2 time.Time
	h.onSleep = func(n int) {
		switch n {
		case 1:
			_, w1 = h.served()
			out1 = h.clk.now()
			require.Equal(t, 2, h.prepares, "build 1: provisional and restamped Prepare")
		case 2:
			_, w2 = h.served()
			out2 = h.clk.now()
			require.Equal(t, 3, h.prepares, "build 2 (steady state): one Prepare")
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 2)
	require.Equal(t, t0.Add(10*time.Minute), h.calls[0].nextBuildAt, "Build is given the provisional bare tick")
	require.Equal(t, t0.Add(10*time.Minute+32*time.Second), w1.NextBuildAt, "table 1: ceil(tick + own 31.7 s)")
	require.Equal(t, t0.Add(20*time.Minute+32*time.Second), w2.NextBuildAt, "table 2: lastBuild is own (31.7 s), not the first build's extra work")
	require.Equal(t, t0.Add(20*time.Minute+32*time.Second), h.calls[1].nextBuildAt)
	require.Equal(t, t0.Add(10*time.Minute), h.calls[1].at, "the restamp's work does not move the grid")
	require.Equal(t, t0.Add(33400*time.Millisecond), out1, "table 1 is out after build, Prepare, restamp, Prepare")
	require.False(t, out2.After(w1.NextBuildAt), "table 2 (out at %s) is out by table 1's nextBuildAt %s", out2, w1.NextBuildAt)
	// build_ms is the build alone, duration_ms what the next stamp counts (build + one Prepare),
	// the same on the first build as in steady state.
	log := h.logs.String()
	require.Equal(t, 2, strings.Count(log, "build_ms=30500 duration_ms=31700"), log)
}

// A build longer than one cycle: the first table names the tick after its own end, not the
// provisional next tick.
func TestHubRefresher_FirstTableOverrunNamesTheTickAfterItsEnd(t *testing.T) {
	h, r, ctx := newHubHarness(t, time.Minute)
	t0 := h.clk.now()
	h.pubCost = 1200 * time.Millisecond
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 70500*time.Millisecond) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step}
	var w1 wireTable
	h.onSleep = func(n int) {
		_, w1 = h.served()
		h.cancel()
	}
	r.Run(ctx)
	// own = 71.7 s: the build ends past tick 1 (60 s), so the next build starts at 120 s and
	// table 2 is out at 120 s + 71.7 s, rounded up to 192 s. The provisional tick would say 132 s.
	require.Equal(t, t0.Add(192*time.Second), w1.NextBuildAt)
}

// A real restart on one shared clock: a client holds the old process's table; the new process
// (a new dataset, a new API, nothing kept) answers it with its own table 1, which names the
// new build's own duration, and a poll at that nextBuildAt gets table 2.
func TestHubRefresher_FirstTableAfterRestartIsOutAtItsNextBuildAt(t *testing.T) {
	const every = 10 * time.Minute
	a, ra, ctxA := newHubHarness(t, every)
	a.pubCost = 1200 * time.Millisecond
	fast := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 20*time.Second) }
	a.script = []func(hubCall) (hubprices.Result, time.Duration){fast, fast}
	var etagA string
	a.onSleep = func(n int) {
		if n == 2 {
			rec, _ := a.served()
			etagA = rec.Header().Get("ETag")
			a.cancel()
		}
	}
	ra.Run(ctxA)
	require.NotEmpty(t, etagA)

	b, rb, ctxB := newHubHarness(t, every)
	b.clk = a.clk // the same wall clock: B is a new process on the same machine
	b.clk.add(3 * time.Minute)
	b.pubCost = 1200 * time.Millisecond
	slow := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30500*time.Millisecond) }
	b.script = []func(hubCall) (hubprices.Result, time.Duration){slow, slow}
	var etagB1 string
	var w1 wireTable
	b.onSleep = func(n int) {
		switch n {
		case 1:
			var rec *httptest.ResponseRecorder
			rec, w1 = b.served("If-None-Match", etagA)
			require.Equal(t, http.StatusOK, rec.Code, "the client's poll with the old process's ETag gets B's table 1")
			etagB1 = rec.Header().Get("ETag")
			require.NotEqual(t, etagA, etagB1)
			require.Equal(t, w1.BuiltAt.Add(10*time.Minute+32*time.Second), w1.NextBuildAt,
				"B's own 31.7 s: not the bare tick, not A's 21.2 s")
		case 2:
			delta := w1.NextBuildAt.Sub(b.clk.now())
			require.GreaterOrEqual(t, int64(delta), int64(0), "table 2 is out before table 1's nextBuildAt")
			b.clk.add(delta)
			rec, _ := b.served("If-None-Match", etagB1)
			b.clk.add(-delta)
			require.Equal(t, http.StatusOK, rec.Code, "a poll at table 1's nextBuildAt gets table 2")
			b.cancel()
		}
	}
	rb.Run(ctxB)
	require.Len(t, b.calls, 2)
}

// A shutdown during the provisional Prepare publishes nothing.
func TestHubRefresher_CancelDuringFirstPrepareDoesNotPublish(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.pubCost = time.Second
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onPrepare = func(n int) { h.cancel() }
	r.Run(ctx)
	require.Equal(t, 1, h.prepares)
	rec, _ := h.served()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// A shutdown during the restamp work (after the provisional Prepare, in the second Prepare)
// publishes nothing either.
func TestHubRefresher_CancelDuringRestampDoesNotPublish(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onPrepare = func(n int) {
		if n == 2 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Equal(t, 2, h.prepares)
	rec, _ := h.served()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// A Restamp error publishes the provisional table (today's stamp, the bare tick), warns, and
// the measured duration still feeds the next stamp.
func TestHubRefresher_RestampErrorPublishesTheProvisionalTable(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.pubCost = 1200 * time.Millisecond
	h.restampErr = errors.New("boom")
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30500*time.Millisecond) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step}
	var w1 wireTable
	h.onSleep = func(n int) {
		if n == 1 {
			var rec *httptest.ResponseRecorder
			rec, w1 = h.served()
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, h.prepares, "no second Prepare after a failed restamp")
		} else {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Equal(t, t0.Add(10*time.Minute), w1.NextBuildAt)
	require.Contains(t, h.logs.String(), "first table not restamped")
	require.Equal(t, t0.Add(20*time.Minute+32*time.Second), h.calls[1].nextBuildAt, "lastBuild is the measured own duration")
}
