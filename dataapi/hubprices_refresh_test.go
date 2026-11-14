package dataapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/hubprices"
)

type hubCall struct {
	prev                 *hubprices.Table
	builtAt, nextBuildAt time.Time
	at                   time.Time // the clock when Build started
	attempt              time.Time // the dataset's NextAttempt when Build started
}

// hubHarness runs a HubRefresher on a fake clock: Sleep advances the clock, Build is scripted.
type hubHarness struct {
	t       *testing.T
	clk     *stepClock
	mem     *dataapi.MemoryDataset
	api     *dataapi.API
	cancel  context.CancelFunc
	calls   []hubCall
	sleeps  []time.Duration
	script  []func(c hubCall) (res hubprices.Result, took time.Duration)
	onSleep func(n int)               // n: 1-based count of sleeps so far, runs before the clock moves
	late    func(n int) time.Duration // how late the n-th wake-up is (nil: on time)
	wall    time.Duration             // a step of the wall clock: every read after it is set moves by it
	busy    atomic.Int32
}

// now is the harness clock: the step clock with its wall time moved by h.wall; the monotonic
// reading (when the clock has one) does not move.
func (h *hubHarness) now() time.Time {
	t := h.clk.now()
	if h.wall != 0 {
		t = wallStepped(t, h.wall)
	}
	return t
}

// wallStepped is t after the system clock was stepped by d: the wall time moves by d, the
// monotonic reading stays, as time.Now returns after an NTP step or a VM restore. The time
// package has no API for it, so this copies the monotonic field back over the one Add moved
// (time.Time is {wall uint64; ext int64; loc *Location}, and ext holds the monotonic reading
// when there is one). TestHubRefresher_WallClockStepMovesStampsNotCadence checks the layout.
func wallStepped(t time.Time, d time.Duration) time.Time {
	type timeLayout struct {
		wall uint64
		ext  int64
		loc  *time.Location
	}
	s := t.Add(d)
	(*timeLayout)(unsafe.Pointer(&s)).ext = (*timeLayout)(unsafe.Pointer(&t)).ext
	return s
}

func (h *hubHarness) Build(ctx context.Context, prev *hubprices.Table, builtAt, nextBuildAt time.Time) (hubprices.Result, error) {
	if h.busy.Add(1) != 1 {
		h.t.Error("two builds at once")
	}
	defer h.busy.Add(-1)
	c := hubCall{prev: prev, builtAt: builtAt, nextBuildAt: nextBuildAt, at: h.now(), attempt: h.mem.NextAttempt()}
	h.calls = append(h.calls, c)
	if len(h.calls) > len(h.script) {
		h.t.Errorf("unexpected build %d", len(h.calls))
		h.cancel()
		return hubprices.Result{}, ctx.Err()
	}
	res, took := h.script[len(h.calls)-1](c)
	h.clk.add(took)
	return res, nil
}

func newHubHarness(t *testing.T, every time.Duration) (*hubHarness, *dataapi.HubRefresher, context.Context) {
	h := &hubHarness{t: t, clk: newStepClock(), mem: dataapi.NewMemoryDataset(dataapi.MarketHubsMaxBytes)}
	h.api = newAPI(t, dataapi.Config{DatasetDir: t.TempDir(), MarketHubs: h.mem, Now: h.now})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	r, err := dataapi.NewHubRefresher(dataapi.HubRefresherConfig{
		Builder: h, Dataset: h.mem, Every: every, Now: h.now, Logger: slog.New(slog.DiscardHandler),
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			if h.onSleep != nil {
				h.onSleep(len(h.sleeps))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			h.clk.add(d)
			if h.late != nil {
				h.clk.add(h.late(len(h.sleeps)))
			}
			return nil
		},
	})
	require.NoError(t, err)
	return h, r, ctx
}

// complete is a publishable result whose body carries the times Build was given.
func complete(c hubCall, took time.Duration) (hubprices.Result, time.Duration) {
	body := fmt.Sprintf(`{"format":"market-hubs","version":1,"builtAt":%q,"nextBuildAt":%q,"n":%d}`,
		c.builtAt.UTC().Format(time.RFC3339), c.nextBuildAt.UTC().Format(time.RFC3339), c.at.Unix())
	return hubprices.Result{Table: &hubprices.Table{BuiltAt: c.builtAt, NextBuildAt: c.nextBuildAt}, JSON: []byte(body), Complete: true}, took
}

func incomplete(c hubCall, took time.Duration) (hubprices.Result, time.Duration) {
	return incompleteAt(c, took, time.Time{})
}

// failedAt is incomplete with the given hub statuses.
func failedAt(c hubCall, took time.Duration, st ...hubprices.HubStatus) (hubprices.Result, time.Duration) {
	return hubprices.Result{
		Table:   &hubprices.Table{BuiltAt: c.builtAt, NextBuildAt: c.nextBuildAt},
		Problem: hubprices.ErrIncomplete,
		Status:  st,
	}, took
}

// incompleteAt is incomplete with the failed hub's ESI Expires set to retryAt.
func incompleteAt(c hubCall, took time.Duration, retryAt time.Time) (hubprices.Result, time.Duration) {
	return hubprices.Result{
		Table:   &hubprices.Table{BuiltAt: c.builtAt, NextBuildAt: c.nextBuildAt},
		Problem: hubprices.ErrIncomplete,
		Status:  []hubprices.HubStatus{{Key: "jita", Reason: hubprices.ReasonBackoff, Err: fmt.Errorf("backed off"), RetryAt: retryAt}},
	}, took
}

func (h *hubHarness) get() (int, string, string) {
	rec := do(h.api, http.MethodGet, "/v1/datasets/"+hubsName, "")
	return rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control")
}

func TestHubRefresher_CadenceAndTimes(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 40*time.Second) },
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onSleep = func(n int) {
		if n == 3 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	for i, c := range h.calls {
		require.Equal(t, t0.Add(time.Duration(i)*10*time.Minute), c.builtAt, "builtAt is the tick time")
		require.Equal(t, c.at, c.builtAt)
	}
	// nextBuildAt is the expected publish time of the next table: next tick + the last measured
	// build duration (the next tick alone before any build), no margin.
	require.Equal(t, t0.Add(10*time.Minute), h.calls[0].nextBuildAt)
	require.Equal(t, h.calls[1].builtAt.Add(10*time.Minute+30*time.Second), h.calls[1].nextBuildAt)
	require.Equal(t, h.calls[2].builtAt.Add(10*time.Minute+40*time.Second), h.calls[2].nextBuildAt)
	require.Nil(t, h.calls[0].prev)
	require.Equal(t, []time.Duration{9*time.Minute + 30*time.Second, 9*time.Minute + 20*time.Second, 9*time.Minute + 30*time.Second}, h.sleeps)
}

func TestHubRefresher_PassesPreviousTable(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	var tabs []*hubprices.Table
	step := func(c hubCall) (hubprices.Result, time.Duration) {
		res, d := complete(c, time.Second)
		tabs = append(tabs, res.Table)
		return res, d
	}
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step}
	h.onSleep = func(n int) {
		if n == 2 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 2)
	require.Nil(t, h.calls[0].prev)
	require.Same(t, tabs[0], h.calls[1].prev)
}

func TestHubRefresher_FirstIncompleteIsRetriedAtExpires(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		// The failed region's Expires is 2 minutes after the start; the build ends at 30 s.
		func(c hubCall) (hubprices.Result, time.Duration) {
			return incompleteAt(c, 30*time.Second, t0.Add(2*time.Minute))
		},
		// No Expires from ESI: the 300 s fallback.
		func(c hubCall) (hubprices.Result, time.Duration) { return incomplete(c, 30*time.Second) },
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onSleep = func(n int) {
		if n < 3 {
			require.Equal(t, h.clk.now().Add(h.sleeps[n-1]), h.mem.NextAttempt(), "the next attempt is published for Retry-After")
		}
		switch n {
		case 1, 2:
			code, _, _ := h.get()
			require.Equal(t, http.StatusServiceUnavailable, code, "nothing is published from an incomplete build")
		case 3:
			code, body, cc := h.get()
			require.Equal(t, http.StatusOK, code)
			require.Contains(t, body, h.calls[2].builtAt.UTC().Format(time.RFC3339))
			require.Equal(t, "public, max-age=570", cc) // body nextBuildAt (tick+10m: no complete build measured yet) minus the 30 s build
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	require.Equal(t, 90*time.Second, h.sleeps[0], "retry at the failed region's Expires")
	require.Equal(t, 300*time.Second, h.sleeps[1], "fallback when ESI gave none")
	require.Equal(t, t0.Add(2*time.Minute), h.calls[1].builtAt, "retry builds are stamped with their own start")
}

func TestHubRefresher_RetryAtInThePastFallsBack(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		// The failed region's Expires passed during the 30 s build: retrying at it would be a
		// retry in the past, so the 300 s fallback applies.
		func(c hubCall) (hubprices.Result, time.Duration) {
			return incompleteAt(c, 30*time.Second, t0.Add(10*time.Second))
		},
	}
	h.onSleep = func(int) { h.cancel() }
	r.Run(ctx)
	require.Equal(t, []time.Duration{300 * time.Second}, h.sleeps)
}

func TestHubRefresher_UpdatedHubRetryAtIsIgnored(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		// jita updated (its Expires is far out) and amarr failed with no Expires: the retry is
		// the fallback, not jita's Expires.
		func(c hubCall) (hubprices.Result, time.Duration) {
			return failedAt(c, 30*time.Second,
				hubprices.HubStatus{Key: "jita", Updated: true, RetryAt: t0.Add(20 * time.Minute)},
				hubprices.HubStatus{Key: "amarr", Reason: hubprices.ReasonError, Err: fmt.Errorf("500")})
		},
	}
	h.onSleep = func(int) { h.cancel() }
	r.Run(ctx)
	require.Equal(t, []time.Duration{300 * time.Second}, h.sleeps)
}

func TestHubRefresher_FailedBuildDoesNotChangeTheMeasuredDuration(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
		func(c hubCall) (hubprices.Result, time.Duration) { return incomplete(c, time.Second) }, // every hub backed off at once
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onSleep = func(n int) {
		if n == 3 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	require.Equal(t, h.calls[2].builtAt.Add(10*time.Minute+30*time.Second), h.calls[2].nextBuildAt,
		"the expected publish time uses the last complete build, not the 1 s failed one")
}

func TestHubRefresher_LateWakeUpsStayOnTheGrid(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step, step, step}
	h.late = func(n int) time.Duration {
		if n == 3 {
			return 12 * time.Minute // the process stalled through a whole tick
		}
		return 7 * time.Second
	}
	h.onSleep = func(n int) {
		if n == 4 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 4)
	for i, want := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute} {
		require.Equal(t, t0.Add(want), h.calls[i].builtAt, "build %d lands on the planned grid", i)
	}
	require.Equal(t, t0.Add(10*time.Minute+7*time.Second), h.calls[1].at, "the wake-up itself was late")
	require.Equal(t, []time.Duration{
		9*time.Minute + 30*time.Second,
		9*time.Minute + 23*time.Second, // from 10:37 to the planned 20:00, not 20:07
		9*time.Minute + 23*time.Second,
		7*time.Minute + 30*time.Second, // woke at 42:00 (12 min late): the 40:00 tick, done 42:30, next 50:00
	}, h.sleeps)
}

func TestHubRefresher_NextAttemptIsSetAtBuildStart(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) {
			code, _, _ := h.get()
			require.Equal(t, http.StatusServiceUnavailable, code)
			return incomplete(c, 30*time.Second)
		},
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 40*time.Second) },
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 40*time.Second) },
	}
	h.onSleep = func(n int) {
		if n == 3 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	// No complete build yet: the planned end is the 300 s fallback.
	require.Equal(t, h.calls[0].at.Add(300*time.Second), h.calls[0].attempt)
	require.Equal(t, h.calls[1].at.Add(300*time.Second), h.calls[1].attempt, "an incomplete build measures nothing")
	// After a complete build: start plus its measured duration.
	require.Equal(t, h.calls[2].at.Add(40*time.Second), h.calls[2].attempt)
}

func TestHubRefresher_LaterIncompleteKeepsPublishedTable(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	var kept string
	var tabs []*hubprices.Table
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) {
			res, d := complete(c, 30*time.Second)
			tabs = append(tabs, res.Table)
			return res, d
		},
		func(c hubCall) (hubprices.Result, time.Duration) {
			res, d := incomplete(c, 30*time.Second)
			tabs = append(tabs, res.Table)
			return res, d
		},
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onSleep = func(n int) {
		switch n {
		case 1:
			_, kept, _ = h.get()
			require.NotEmpty(t, kept)
		case 2:
			code, body, _ := h.get()
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, kept, body, "the incomplete build leaves the served table alone")
		case 3:
			_, body, _ := h.get()
			require.NotEqual(t, kept, body)
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	require.Equal(t, 10*time.Minute-30*time.Second, h.sleeps[1], "after a published table, the next cycle tick (no 2-minute retry)")
	require.Same(t, tabs[1], h.calls[2].prev, "keep-last per hub rides on the last built table")
}

func TestHubRefresher_OverrunSkipsTicks(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 25*time.Minute) },
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, time.Minute) },
	}
	h.onSleep = func(n int) {
		if n == 2 {
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 2, "ticks at 10 and 20 minutes fell inside the first build and are skipped")
	require.Equal(t, 5*time.Minute, h.sleeps[0])
	require.Equal(t, t0.Add(30*time.Minute), h.calls[1].builtAt)
	// The measured duration (25 min) enters the next expected publish time: a 25-minute build
	// from 30:00 ends at 55:00, skips the 40 and 50 ticks, and the next one (60:00) publishes
	// at 85:00.
	require.Equal(t, h.calls[1].builtAt.Add(30*time.Minute+25*time.Minute), h.calls[1].nextBuildAt)
}

func TestHubRefresher_LateWakeUpNextBuildAtIsTheNextPublish(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	t0 := h.clk.now()
	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step, step}
	h.late = func(n int) time.Duration {
		if n == 1 {
			return 9*time.Minute + 40*time.Second // woke at 19:40 for the 10:00 tick
		}
		return 0
	}
	var cc string
	h.onSleep = func(n int) {
		switch n {
		case 2:
			_, _, cc = h.get() // at 20:10, just after the late build published
		case 3:
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	late := h.calls[1]
	require.Equal(t, t0.Add(10*time.Minute), late.builtAt, "the late build keeps its tick")
	published := late.at.Add(30 * time.Second)
	require.True(t, late.nextBuildAt.After(published), "nextBuildAt %s is after the publish at %s", late.nextBuildAt, published)
	require.Equal(t, t0.Add(30*time.Minute), h.calls[2].builtAt, "the 20:00 tick fell inside the late build")
	require.Equal(t, h.calls[2].attempt, late.nextBuildAt, "nextBuildAt is the next build's planned end")
	require.Equal(t, "public, max-age=620", cc, "from 20:10 to the next publish at 30:30, not 20 s to 20:30")
}

func TestHubRefresher_WallClockStepMovesStampsNotCadence(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	// A clock reading with a monotonic part, as in production, on a whole wall second.
	base := time.Now()
	h.clk.t = base.Add(-time.Duration(base.Nanosecond()))
	t0 := h.now()
	require.Contains(t, t0.String(), " m=", "the clock carries a monotonic reading")
	stepped := wallStepped(t0, time.Hour)
	require.Zero(t, stepped.Sub(t0), "time.Time layout: the monotonic reading stays")
	require.Equal(t, time.Hour, stepped.Round(0).Sub(t0.Round(0)), "time.Time layout: the wall time moves")

	step := func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) }
	h.script = []func(hubCall) (hubprices.Result, time.Duration){step, step, step}
	var cc string
	h.onSleep = func(n int) {
		switch n {
		case 1:
			h.wall = time.Hour // the system clock is stepped by +1 h during the first wait
		case 2:
			_, _, cc = h.get()
		case 3:
			h.cancel()
		}
	}
	r.Run(ctx)
	require.Len(t, h.calls, 3)
	require.Equal(t, []time.Duration{9*time.Minute + 30*time.Second, 9*time.Minute + 30*time.Second, 9*time.Minute + 30*time.Second},
		h.sleeps, "the cadence runs on the monotonic clock")
	w0 := t0.UTC()
	require.Equal(t, w0, h.calls[0].builtAt.UTC())
	for i := 1; i < 3; i++ {
		want := w0.Add(time.Hour + time.Duration(i)*10*time.Minute)
		require.Equal(t, want, h.calls[i].builtAt.UTC(), "build %d: builtAt follows the new wall time", i)
		require.Equal(t, h.calls[i].at.UTC(), h.calls[i].builtAt.UTC(), "build %d started on its tick", i)
		require.Equal(t, want.Add(10*time.Minute+30*time.Second), h.calls[i].nextBuildAt, "build %d: nextBuildAt on the new wall time", i)
	}
	require.Equal(t, "public, max-age=600", cc, "max-age from the stepped wall time on both sides")
}

func TestHubRefresher_PublishedNextBuildEqualsBody(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, 30*time.Second) },
	}
	h.onSleep = func(n int) {
		_, body, cc := h.get()
		var w struct {
			NextBuildAt time.Time `json:"nextBuildAt"`
		}
		require.NoError(t, jsonUnmarshal(body, &w))
		want := int(w.NextBuildAt.Sub(h.clk.now()) / time.Second)
		require.Equal(t, fmt.Sprintf("public, max-age=%d", want), cc, "the route's max-age derives from the body's nextBuildAt")
		h.cancel()
	}
	r.Run(ctx)
}

func TestHubRefresher_CancelDuringBuildPublishesNothing(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) {
			h.cancel() // shutdown arrives mid-build; the build still returns a complete result
			return complete(c, time.Second)
		},
	}
	r.Run(ctx)
	require.Len(t, h.calls, 1)
	code, _, _ := h.get()
	require.Equal(t, http.StatusServiceUnavailable, code, "a result that arrived after the cancel is not published")
}

func TestHubRefresher_RunReturnsWhenCanceledWhileWaiting(t *testing.T) {
	h, r, ctx := newHubHarness(t, 10*time.Minute)
	h.script = []func(hubCall) (hubprices.Result, time.Duration){
		func(c hubCall) (hubprices.Result, time.Duration) { return complete(c, time.Second) },
	}
	h.onSleep = func(int) { h.cancel() }
	r.Run(ctx)
	require.Len(t, h.calls, 1)
}

func TestNewHubRefresher_RefusesBadConfig(t *testing.T) {
	ok := dataapi.HubRefresherConfig{Builder: &hubHarness{}, Dataset: dataapi.NewMemoryDataset(10), Every: time.Minute}
	_, err := dataapi.NewHubRefresher(ok)
	require.NoError(t, err)
	for name, mut := range map[string]func(*dataapi.HubRefresherConfig){
		"no builder": func(c *dataapi.HubRefresherConfig) { c.Builder = nil },
		"no dataset": func(c *dataapi.HubRefresherConfig) { c.Dataset = nil },
		"no cycle":   func(c *dataapi.HubRefresherConfig) { c.Every = 0 },
	} {
		c := ok
		mut(&c)
		_, err := dataapi.NewHubRefresher(c)
		require.Error(t, err, name)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
