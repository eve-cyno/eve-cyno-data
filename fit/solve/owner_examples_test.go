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
// that fails on real behaviour goes to review. It is re-expected only for an owner ruling,
// and the comment of the test names the ruling and the date (the Cerberus example, 2026-11-09).

const (
	typeArbalestHML   = 8105  // 'Arbalest' Heavy Missile Launcher: Tech I, meta level 4
	typeDominationHML = 13921 // Domination Heavy Missile Launcher: Faction, meta level 7
	hullVexor         = 626
	hullThoraxID      = 627
	hullMyrmidon      = 24700
	hullBrutix        = 16229
)

// Fix for a character: Cerberus (reference 3) for the missiler pilot, fallback policy. The
// Heavy Missile Launcher II lines are unusable (no Heavy Missile Specialization).
//
// 2026-11-09, owner ruling 4b (any meta level or group when the fit is viable; only an Officer
// module warns): the pick is now the Faction 'Domination' Heavy Missile Launcher (meta group 4,
// meta level 7) on all six lines, DPS 223.969 (+3.2 % over the B2 216.970), EHP unchanged. It
// supersedes the 'Arbalest' pick (8105, Tech I, DPS equal to B2) of the earlier Tech-only
// candidate rule; that alternative stays valid and is anchored in
// TestOwnerExample_FixForCharacter_TechOnlyKeepsArbalest.
func TestOwnerExample_FixForCharacter(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 3)
	require.Equal(t, hullCerberus, f.HullID)
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
		Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["missiler"]})
	require.NoError(t, err)
	require.Equal(t, StatusOK, res.Status)
	require.Len(t, res.Changes, 6)
	for _, ch := range res.Changes {
		require.Equal(t, SlotHigh, ch.Group)
		require.Equal(t, typeDominationHML, ch.To)
		require.NotZero(t, ch.From)
		require.False(t, ch.Downgrade, "%+v", ch)
	}
	require.Equal(t, 4, c.MetaGroup(typeDominationHML), "Faction")
	require.Equal(t, 6, countType(res.Fit.High, typeDominationHML))
	require.False(t, res.Baseline.Flown.Valid, "the written fit is not valid for this pilot")
	require.True(t, res.Result.Valid)
	require.InDelta(t, 248.8542, res.Result.DPS, 5e-4) // was 223.969 before the Cerberus kinetic fix (M1)
	require.Greater(t, res.Result.DPS, res.Baseline.Flown.DPS)
	require.InDelta(t, 241.0775, res.Baseline.Flown.DPS, 5e-4) // was 216.970 before M1
	require.InDelta(t, 50364.122, res.Result.EHP, 5e-4)
	require.InDelta(t, res.Baseline.Flown.EHP, res.Result.EHP, 1e-6)
	require.True(t, res.Result.CapStable)
	require.Empty(t, res.Uncertain)
	require.Empty(t, res.Warnings)
	// The evaluation count follows the candidate lists and the search reductions (twin lines,
	// the repair filter); it is bounded here and logged, not frozen. Measured 79 on 2026-11-09.
	t.Logf("%d evaluations", res.Evaluations)
	require.Greater(t, res.Evaluations, 6, "more than the baselines and one change per line")
	require.Less(t, res.Evaluations, 150, "far below the cap of %d", CapInteractive)
	require.False(t, res.Capped)

	// The rest of the fit is unchanged.
	want := f
	want.High = append([]fit.FitModule(nil), f.High...)
	for _, ch := range res.Changes {
		want.High[ch.Index].TypeID = typeDominationHML
		want.High[ch.Index].Name = res.Fit.High[ch.Index].Name
	}
	require.Equal(t, want.High, res.Fit.High)
	require.Equal(t, f.Mid, res.Fit.Mid)
	require.Equal(t, f.Low, res.Fit.Low)
	require.Equal(t, f.Rig, res.Fit.Rig)
	require.Equal(t, f.Drones, res.Fit.Drones)
}

// The superseded Tech-I pick stays a valid answer: with only Tech I and Tech II candidates
// (techCatalog) the same request returns the 'Arbalest' on all six lines, DPS equal to B2.
func TestOwnerExample_FixForCharacter_TechOnlyKeepsArbalest(t *testing.T) {
	c := techCatalog(t)
	f := refFit(t, c, 3)
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
		Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["missiler"]})
	require.NoError(t, err)
	require.Equal(t, StatusOKWithDowngrades, res.Status)
	require.Len(t, res.Changes, 6)
	for _, ch := range res.Changes {
		require.Equal(t, typeArbalestHML, ch.To)
		require.True(t, ch.Downgrade, "%+v", ch)
	}
	require.Equal(t, 6, countType(res.Fit.High, typeArbalestHML))
	require.True(t, res.Result.Valid)
	require.InDelta(t, res.Baseline.Flown.DPS, res.Result.DPS, 1e-6)
	require.InDelta(t, 241.0775, res.Result.DPS, 5e-4) // was 216.970 before M1
	require.InDelta(t, res.Baseline.Flown.EHP, res.Result.EHP, 1e-6)
	require.Empty(t, res.Uncertain)
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
	skipHeavyRace(t)
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

// Owner ruling 2026-11-09 (4b, d) on the real SDE: Downgrade may move a usable line to any meta
// level or group when the fit is viable (the earlier rule, no move up for a usable line, is
// dropped). It always returns a valid fit for the reference fits under fitIII, and it
// introduces no Officer module (an Officer line is ranked after every other choice).
func TestDowngrade_AnyMetaGroupButNoOfficerIntroduced(t *testing.T) {
	skipHeavyRace(t)
	c := realCatalog(t)
	sk := e3aProfiles(t)["fitIII"]
	for _, n := range []int{1, 2, 3, 5, 6} {
		f := refFit(t, c, n)
		res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
			Request{Kind: KindDowngrade, Fit: f, Skills: sk})
		require.NoError(t, err)
		if res.Status == StatusNoCandidate { // no usable fitting at all for this pilot
			t.Logf("ref %d: no_candidate, %d evaluations", n, res.Evaluations)
			continue
		}
		require.True(t, res.Result.Valid, "ref %d", n)
		ups := 0
		for _, ch := range res.Changes {
			if ch.To != 0 {
				require.False(t, c.IsOfficer(ch.To) && !c.IsOfficer(ch.From), "ref %d: %+v introduces an Officer module", n, ch)
				if c.MetaGroup(ch.To) != c.MetaGroup(ch.From) && c.MetaLevel(ch.To) > c.MetaLevel(ch.From) {
					ups++
				}
			}
		}
		t.Logf("ref %d: %s, %d changes, %d moves to a higher meta level in another group, %d evaluations", n, res.Status, len(res.Changes), ups, res.Evaluations)
	}
}
