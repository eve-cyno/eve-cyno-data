package solve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// The owner's two examples (plan 2026-11-07-dp2-fit-l4-l6, T5.8), real SDE. An expectation
// that fails on real behaviour goes to review; it is never re-expected here.

const (
	typeArbalestHML = 8105 // 'Arbalest' Heavy Missile Launcher: Tech I, meta level 4
	hullVexor       = 626
	hullThoraxID    = 627
	hullMyrmidon    = 24700
	hullBrutix      = 16229
)

// Fix for a character: Cerberus (reference 3) for the missiler pilot, fallback policy. The
// Heavy Missile Launcher II lines are unusable (no Heavy Missile Specialization); the pick
// is the Tech I 'Arbalest' on all six, which keeps DPS and EHP.
func TestOwnerExample_FixForCharacter(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 3)
	require.Equal(t, hullCerberus, f.HullID)
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
		Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["missiler"]})
	require.NoError(t, err)
	require.Equal(t, StatusOKWithDowngrades, res.Status)
	require.Len(t, res.Changes, 6)
	for _, ch := range res.Changes {
		require.Equal(t, SlotHigh, ch.Group)
		require.Equal(t, typeArbalestHML, ch.To)
		require.NotZero(t, ch.From)
		require.True(t, ch.Downgrade, "%+v", ch)
	}
	require.Equal(t, 6, countType(res.Fit.High, typeArbalestHML))
	require.False(t, res.Baseline.Flown.Valid, "the written fit is not valid for this pilot")
	require.True(t, res.Result.Valid)
	require.InDelta(t, res.Baseline.Flown.DPS, res.Result.DPS, 1e-6)
	require.InDelta(t, 216.970, res.Result.DPS, 5e-4)
	require.InDelta(t, 50364.1, res.Result.EHP, 0.05)
	require.InDelta(t, res.Baseline.Flown.EHP, res.Result.EHP, 1e-6)
	require.Empty(t, res.Uncertain)

	// The rest of the fit is unchanged.
	want := f
	want.High = append([]fit.FitModule(nil), f.High...)
	for _, ch := range res.Changes {
		want.High[ch.Index].TypeID = typeArbalestHML
		want.High[ch.Index].Name = "'Arbalest' Heavy Missile Launcher"
	}
	require.Equal(t, want.High, res.Fit.High)
	require.Equal(t, f.Mid, res.Fit.Mid)
	require.Equal(t, f.Low, res.Fit.Low)
	require.Equal(t, f.Rig, res.Fit.Rig)
	require.Equal(t, f.Drones, res.Fit.Drones)
}

func countType(mods []fit.FitModule, id int) int {
	n := 0
	for _, m := range mods {
		if m.TypeID == id {
			n += max(m.Qty, 1)
		}
	}
	return n
}

// Convert a doctrine collection: references 1, 2, 5, 6 onto four Gallente hulls for the
// droner pilot.
func TestOwnerExample_ConvertDoctrineCollection(t *testing.T) {
	c := realCatalog(t)
	droner := e3aProfiles(t)["droner"]
	hulls := []int{hullVexor, hullThoraxID, hullMyrmidon, hullBrutix}
	var reqs []Request
	for _, ref := range []int{1, 2, 5, 6} {
		f := refFit(t, c, ref)
		for _, h := range hulls {
			reqs = append(reqs, Request{Kind: KindConvert, Fit: f, TargetHull: h, Skills: droner})
		}
	}
	require.Len(t, reqs, 16)

	ctx := context.Background()
	st := c.Store()
	var singles []*Result
	for i, r := range reqs {
		res, err := Solve(ctx, c, NewEngineEvaluator(gofa.New(st), 4), r)
		require.NoError(t, err)
		singles = append(singles, res)
		t.Logf("%2d %-8s -> %-8s %s, %d changes, %d evaluations", i, r.Fit.HullName, c.typeName(r.TargetHull), res.Status, len(res.Changes), res.Evaluations)
		require.NotEqual(t, StatusNoCandidate, res.Status, "pair %d: %s -> %d", i, r.Fit.HullName, r.TargetHull)
		require.NotNil(t, res.Fit)
		require.Equal(t, r.TargetHull, res.Fit.HullID)
		require.True(t, res.Result.Valid, "pair %d", i)

		// Valid under droner: an independent check of the chosen draft.
		viol, err := validUnder(ctx, st, *res.Fit, droner)
		require.NoError(t, err)
		require.Empty(t, viol, "pair %d is valid under droner", i)

		// uncertain exactly when the chosen fit has an unmodelled entry on dps, tank or capacitor.
		stats, err := gofa.New(st).Stats(ctx, *res.Fit, fit.StatsOpts{Skills: droner, MissingAsZero: true})
		require.NoError(t, err)
		unmod := uncertainOf(stats)
		require.Equal(t, len(unmod) > 0, res.Status == StatusUncertain, "pair %d: unmodelled %v, status %s", i, unmod, res.Status)
		if res.Status == StatusUncertain {
			require.Equal(t, unmod, res.Uncertain)
		}
	}

	// The batch through EvaluateMany (one Evaluator shared across the pairs, many workers)
	// equals the single solves byte for byte.
	ev := NewEngineEvaluator(gofa.New(st), 8)
	for i, r := range reqs {
		res, err := Solve(ctx, c, ev, r)
		require.NoError(t, err)
		a, err := json.Marshal(singles[i])
		require.NoError(t, err)
		b, err := json.Marshal(res)
		require.NoError(t, err)
		require.Equal(t, string(a), string(b), "pair %d", i)
	}
}

// validUnder returns the violations of a fit under a pilot, from a full (unpruned) engine
// reading of its resources.
func validUnder(ctx context.Context, st *dogma.Store, f fit.Fit, skills fit.SkillProfile) ([]fit.Violation, error) {
	e := gofa.New(st)
	opts := fit.StatsOpts{Skills: skills, MissingAsZero: true}
	res, err := e.Resources(ctx, f, opts)
	if err != nil {
		return nil, err
	}
	rep := fit.ValidateWith(ctx, st, fixedResources(res), f, nil, false, opts)
	out := append([]fit.Violation(nil), rep.Violations...)
	out = append(out, fit.SkillViolations(ctx, st, f, opts)...)
	for _, d := range []struct {
		code fit.Code
		pool fit.Pool
	}{{CodeDroneBayOver, res.DroneBay}, {CodeDroneBandwidthOver, res.DroneBandwidth}} {
		if d.pool.Used > d.pool.Cap+1e-6 {
			out = append(out, fit.Violation{Code: d.code, Load: d.pool.Used, Capacity: d.pool.Cap})
		}
	}
	return out, nil
}

// H1 on the real SDE: Downgrade never moves a line the pilot can use to a higher meta level
// (Rifter AutoCannon I to AutoCannon II was the defect). Rifter (reference 1) under fitIII is
// valid except for 9.8 CPU over, which only a higher-meta swap or a dropped module would
// clear: Downgrade has neither, so it reports no_candidate.
func TestDowngrade_NeverUpgradesUsableLines(t *testing.T) {
	c := realCatalog(t)
	sk := e3aProfiles(t)["fitIII"]
	for _, n := range []int{1, 2, 3, 5, 6} {
		f := refFit(t, c, n)
		res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
			Request{Kind: KindDowngrade, Fit: f, Skills: sk})
		require.NoError(t, err)
		if n == 1 {
			require.Equal(t, StatusNoCandidate, res.Status)
		}
		for _, ch := range res.Changes {
			if ch.To != 0 && c.CanUse(sk, ch.From) {
				require.LessOrEqual(t, c.MetaLevel(ch.To), c.MetaLevel(ch.From), "ref %d: %+v goes up", n, ch)
			}
		}
	}
}
