package dataapi_test

import (
	"net/http"
	"net/http/httptest"
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
		if n > 2 { // table 1 stamps the bare next tick: no build was measured yet
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
	require.Equal(t, 2, polled)
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
