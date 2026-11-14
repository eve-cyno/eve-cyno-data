package dataapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"eve-cyno.dev/go/data/hubprices"
)

const (
	// hubRetryFallback is the wait after an incomplete first build when ESI named no Expires
	// for the failed regions. Source: the region orders Expires is Last-Modified + 300 s
	// (measured, plan section 3b).
	hubRetryFallback = 300 * time.Second
)

// HubBuilder builds one market-hubs table; *hubprices.Builder is the production one.
type HubBuilder interface {
	Build(ctx context.Context, prev *hubprices.Table, builtAt, nextBuildAt time.Time) (hubprices.Result, error)
}

// HubRefresherConfig configures NewHubRefresher.
type HubRefresherConfig struct {
	Builder HubBuilder
	Dataset *MemoryDataset // the dataset the route serves (Config.MarketHubs)
	Every   time.Duration  // the cycle; must be positive (the caller enforces the minimum)
	Now     func() time.Time
	// Sleep waits d or until ctx ends (returning ctx.Err()); nil means a real timer.
	Sleep  func(ctx context.Context, d time.Duration) error
	Logger *slog.Logger
}

// HubRefresher rebuilds the market-hubs table every cycle and publishes it into a
// MemoryDataset. One build runs at a time: Run is a single loop, so a build that overruns the
// next tick skips the ticks it covered instead of overlapping.
//
// Only a Complete result is published (all five hubs, validated); otherwise the last published
// table stays and the failure is logged. The previous table passed to Build is the last
// one built (published or not), so keep-last per hub survives incomplete builds.
//
// The body's nextBuildAt is the expected publish time of the next table, because the desktop
// asks again at that time: the next build starts at the first grid tick after this build's
// expected end (its start plus the measured duration of the last complete build) and takes
// that duration again. The same second-truncated time goes to MemoryDataset.Publish.
//
// Once a table is published the builds keep to a fixed grid (first build + n * Every): a late
// wake-up does not move the next ticks, and a wake-up later than a whole cycle builds at the
// latest grid tick it passed. Until then a retry runs at retryTime and is stamped with the
// time it starts. The grid runs on the monotonic clock; builtAt and nextBuildAt are stamped
// with the wall time read at the build's start plus the grid point's offset from it, so a
// step of the wall clock after the start (an NTP step at boot, a VM restore) moves the stamps
// with it and leaves the cadence alone.
//
// On shutdown the build's context is canceled and a result that arrives after the cancel is
// not published: a table built from an interrupted run is not worth the risk of a half-fresh
// publish during exit, and the process is going away.
type HubRefresher struct {
	b     HubBuilder
	ds    *MemoryDataset
	every time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	log   *slog.Logger

	prev      *hubprices.Table // last built table
	published bool
	lastBuild time.Duration // measured duration of the last complete build (0 before any)
	lastRes   []hubprices.HubStatus
}

// retryTime is when to retry a build that left nothing published: the latest Expires of the
// hubs that failed, else (or when already past) hubRetryFallback from now. The Expires is
// capped at one cycle (Every) from now: a retry never waits longer than a regular build would,
// so a bad far-future Expires or back-off end from ESI cannot park the refresher (and the
// route's Retry-After, which follows SetNextAttempt) for longer than the normal cadence. The
// fallback needs no cap: it equals the 5 min floor of the cycle (config.MinHubPricesEvery).
func (r *HubRefresher) retryTime() time.Time {
	now := r.now()
	var at time.Time
	for _, s := range r.lastRes {
		if !s.Updated && s.RetryAt.After(at) {
			at = s.RetryAt
		}
	}
	if !at.After(now) {
		return now.Add(hubRetryFallback)
	}
	if limit := now.Add(r.every); at.After(limit) {
		return limit
	}
	return at
}

// NewHubRefresher validates cfg and returns a refresher; call Run to start it.
func NewHubRefresher(cfg HubRefresherConfig) (*HubRefresher, error) {
	switch {
	case cfg.Builder == nil:
		return nil, errors.New("hub refresher: no builder")
	case cfg.Dataset == nil:
		return nil, errors.New("hub refresher: no dataset")
	case cfg.Every <= 0:
		return nil, fmt.Errorf("hub refresher: cycle %s is not positive", cfg.Every)
	}
	r := &HubRefresher{b: cfg.Builder, ds: cfg.Dataset, every: cfg.Every, now: cfg.Now, sleep: cfg.Sleep, log: cfg.Logger}
	if r.now == nil {
		r.now = time.Now
	}
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run builds at once, then at every cycle tick, until ctx ends. It blocks.
func (r *HubRefresher) Run(ctx context.Context) {
	tick := r.now()
	for ctx.Err() == nil {
		r.buildOnce(ctx, tick)
		if ctx.Err() != nil {
			return
		}
		var next time.Time
		scheduled := r.published
		if !scheduled {
			next = r.retryTime()
		} else {
			var skipped int
			next, skipped = r.nextTick(tick, r.now())
			if skipped > 0 {
				r.log.Warn("hub prices: build overran the cycle; ticks skipped", "skipped", skipped)
			}
		}
		r.ds.SetNextAttempt(next)
		if err := r.sleep(ctx, next.Sub(r.now())); err != nil {
			return
		}
		if !scheduled {
			tick = r.now() // a retry is stamped with its own start
			continue
		}
		// The planned tick, not the wake-up time: late wake-ups do not drift the grid. A
		// wake-up past a whole cycle builds at the latest grid tick it passed.
		tick = next
		skipped := 0
		for now := r.now(); !tick.Add(r.every).After(now); tick = tick.Add(r.every) {
			skipped++
		}
		if skipped > 0 {
			r.log.Warn("hub prices: woke up late; ticks skipped", "skipped", skipped)
		}
	}
}

// nextTick is the first grid tick (tick + n * Every, n >= 1) after end, and how many ticks
// it skipped (those at or before end).
func (r *HubRefresher) nextTick(tick, end time.Time) (next time.Time, skipped int) {
	next = tick.Add(r.every)
	for !next.After(end) {
		next = next.Add(r.every)
		skipped++
	}
	return next, skipped
}

// buildOnce runs and, when complete, publishes one build.
func (r *HubRefresher) buildOnce(ctx context.Context, tick time.Time) {
	started := r.now()
	// The next table is published when the next build ends: it starts at the first tick after
	// this build's expected end and takes as long (the next tick alone before any complete
	// build). Stamps are the wall time read now plus the grid point's monotonic offset from now.
	next, _ := r.nextTick(tick, started.Add(r.lastBuild))
	builtAt := started.Add(tick.Sub(started))
	nextBuildAt := started.Add(next.Add(r.lastBuild).Sub(started)).UTC().Truncate(time.Second)
	// The planned end of this build, for the route's Retry-After while nothing is published:
	// the start plus the last complete build's duration, else the fallback.
	planned := hubRetryFallback
	if r.lastBuild > 0 {
		planned = r.lastBuild
	}
	r.ds.SetNextAttempt(started.Add(planned))
	res, err := r.b.Build(ctx, r.prev, builtAt, nextBuildAt)
	took := r.now().Sub(started)
	if res.Table != nil {
		r.prev = res.Table
	}
	if ctx.Err() != nil {
		r.log.Info("hub prices: build canceled; result not published", "error", err)
		return
	}
	r.lastRes = res.Status
	if err != nil {
		r.log.Warn("hub prices: build failed", "error", err, "duration_ms", took.Milliseconds())
		return
	}
	if !res.Complete {
		// An incomplete build is often a fast one (every hub backed off): it says nothing about
		// how long a real build takes, so lastBuild stays.
		r.log.Warn("hub prices: build not complete; the last published table stays",
			append([]any{"problem", res.Problem, "published_before", r.published, "duration_ms", took.Milliseconds()}, statusAttrs(res.Status)...)...)
		return
	}
	r.lastBuild = took
	if err := r.ds.Publish(res.JSON, nextBuildAt); err != nil {
		r.log.Warn("hub prices: publish refused", "error", err, "bytes", len(res.JSON))
		return
	}
	r.published = true
	rows, pages := 0, 0
	for _, s := range res.Status {
		rows += s.Rows
		pages += s.Pages
	}
	r.log.Info("hub prices: published", append([]any{"rows", rows, "pages", pages, "bytes", len(res.JSON),
		"duration_ms", took.Milliseconds(), "built_at", builtAt, "next_build_at", nextBuildAt}, statusAttrs(res.Status)...)...)
}

// statusAttrs renders the per-hub status as one log attribute per hub.
func statusAttrs(st []hubprices.HubStatus) []any {
	out := make([]any, 0, len(st))
	for _, s := range st {
		v := fmt.Sprintf("rows=%d pages=%d updated=%t", s.Rows, s.Pages, s.Updated)
		if s.Reason != "" {
			v += " reason=" + s.Reason
		}
		if s.Err != nil {
			v += " err=" + s.Err.Error()
		}
		if s.Inconsistent {
			v += " inconsistent"
		}
		out = append(out, "hub_"+s.Key, v)
	}
	return out
}
