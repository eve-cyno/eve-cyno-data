package solve

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// offlineEngine is a fakeEngine where an offline high module still draws its load but adds
// no DPS (as gofa does).
type offlineEngine struct{ fakeEngine }

func (e *offlineEngine) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	for i, r := range reqs {
		ev := e.eval(r)
		for _, m := range r.Fit.High {
			if m.State == "offline" {
				ev.Stats.DPS.Theoretical -= e.store.GetDogma(m.TypeID)[attrFakeDPS]
			}
		}
		out[i] = ev
	}
	return out, ctx.Err()
}

// Lines that differ only in the module state are not twins: an offline line does not evaluate
// like an online one, so skipping it changes the result.
func TestSolve_TwinLinesDifferInState(t *testing.T) {
	pg := func(v float64) map[int]float64 { return map[int]float64{attrPGNeed: v} }
	sub := fakeSubset(
		gun(1000, 1, 0, 10, rare(pg(10))),
		gun(1001, 2, 5, 50, rare(pg(10))),
		gun(1002, 1, 4, 50, pg(40)),
		gun(1003, 1, 3, 45, pg(25)),
	)
	c := NewCatalog(sub)
	f := highFit(1001, 1001, 1001)
	f.High[0].State = "offline"
	for _, kind := range []Kind{KindDowngrade, KindRefit} {
		r := Request{Kind: kind, Fit: f, Skills: poor, Policy: fixPolicy(nil)}
		run := func(h *hooks) *Result {
			res, err := solve(context.Background(), c, &offlineEngine{fakeEngine{store: c.Store(), workers: 1}}, r, h)
			require.NoError(t, err)
			return res
		}
		with, without := run(nil), run(&hooks{noSym: true})
		with.Evaluations, without.Evaluations = 0, 0
		a, _ := json.Marshal(with)
		b, _ := json.Marshal(without)
		require.Equal(t, string(b), string(a), kind)
	}
}

// cpuMultEngine is a fakeEngine where attribute 202 (cpuMultiplier) of a fitted low module
// scales the hull's CPU output, as a Co-Processor does in the game.
type cpuMultEngine struct{ fakeEngine }

func (e *cpuMultEngine) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	for i, r := range reqs {
		ev := e.eval(r)
		mult := 1.0
		for _, m := range r.Fit.Low {
			if v, ok := e.store.GetDogma(m.TypeID)[202]; ok && m.State != "offline" {
				mult *= v
			}
		}
		ev.Resources.CPU.Cap *= mult
		out[i] = ev
	}
	return out, ctx.Err()
}

// A module with the same base CPU need but a higher CPU output (every Co-Processor has CPU
// need 0 and a cpuMultiplier) can lower the CPU overrun, so the repair keeps equal-need
// candidates. Three guns draw 105 CPU of 100; Co-Processor II lifts the output to 110.
func TestSolve_RepairKeepsEqualNeedOutputRaiser(t *testing.T) {
	cop := func(id, metaGroup int, level, mult float64) dogma.SubsetType {
		parent := 3000
		if id == 3000 {
			parent = 0
		}
		return withMeta(ty(id, 285, 7, []int{effLow}, map[int]float64{attrMetaLevel: level, attrCPUNeed: 0, attrPGNeed: 1, 202: mult}), metaGroup, parent)
	}
	cpu := func(v float64) map[int]float64 { return map[int]float64{attrCPUNeed: v} }
	sub := fakeSubset(
		gun(1000, 1, 0, 50, cpu(35)),
		gun(1003, 1, 3, 40, cpu(30)),
		cop(3000, 1, 0, 1.0),
		cop(3001, 2, 5, 1.1),
	)
	c := NewCatalog(sub)
	f := highFit(1000, 1000, 1000) // 105 CPU of 100
	f.Low = append(f.Low, f.High[0])
	f.Low[0].TypeID, f.Low[0].Name = 3000, "cop"
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		r := Request{Kind: kind, Fit: f, Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 98))}
		res, err := Solve(context.Background(), c, &cpuMultEngine{fakeEngine{store: c.Store(), workers: 1}}, r)
		require.NoError(t, err)
		require.NotEqual(t, StatusNoCandidate, res.Status, kind)
		require.True(t, res.Result.Valid, kind)
		require.GreaterOrEqual(t, res.Result.DPS, 0.98*150, kind)
	}
}

// Downgrade with a DPS gate of 90 %. Three 1001 guns draw 150 MW of 100 MW. Swapping one gun
// for a 1003 saves 12 MW and loses 0.5 DPS; for a 1004 it saves 17 MW and loses 30 DPS. The
// gate is met by three 1003s only; the most-saved rule would take 1004s and end below it.
func TestSolve_DowngradeWithGatesKeepsLossPerUnitRule(t *testing.T) {
	pg := func(v float64) map[int]float64 { return map[int]float64{attrPGNeed: v} }
	sub := fakeSubset(
		gun(1000, 1, 0, 10, rare(pg(10))),
		gun(1001, 2, 5, 50, rare(pg(10))),
		gun(1002, 1, 4, 50, pg(130.0/3)),
		gun(1003, 1, 3, 49.5, pg(130.0/3-12)),
		gun(1004, 1, 2, 20, pg(130.0/3-17)),
	)
	res := fakeSolve(t, sub, Request{Kind: KindDowngrade, Fit: highFit(1001, 1001, 1001), Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 90))})
	require.NotEqual(t, StatusNoCandidate, res.Status)
	require.Equal(t, []int{1003, 1003, 1003}, highTypes(res.Fit))
}

// A Downgrade without gates repairs by the most of the binding saved. Three 1001 guns draw
// 135 MW of 100 MW. The greedy run (1003 saves the most powergrid, 38 MW, but needs 45 CPU)
// ends without an accepted fit; the run with the loss per unit rule finds one, and the solve
// must return it.
func TestSolve_DowngradeRetriesWhenTheGreedyRunEndsUnaccepted(t *testing.T) {
	pgcpu := func(pg, cpu float64) map[int]float64 { return map[int]float64{attrPGNeed: pg, attrCPUNeed: cpu} }
	sub := fakeSubset(
		gun(1000, 1, 0, 10, rare(pgcpu(27, 60))),
		gun(1001, 2, 5, 50, rare(pgcpu(45, 20))),
		gun(1002, 1, 4, 37, pgcpu(42, 24)),
		gun(1003, 1, 3, 11, pgcpu(7, 45)),
		gun(1004, 1, 2, 41, pgcpu(54, 17)),
	)
	c := NewCatalog(sub)
	r := Request{Kind: KindDowngrade, Fit: highFit(1001, 1001, 1001), Skills: poor, Policy: fixPolicy(nil)}
	run := func(h *hooks) *Result {
		res, err := solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: 1}, r, h)
		require.NoError(t, err)
		return res
	}
	greedy := run(&hooks{noRetry: true})
	require.Equal(t, StatusNoCandidate, greedy.Status, "the scenario must end the greedy run unaccepted")
	res := run(nil)
	require.NotEqual(t, StatusNoCandidate, res.Status)
	require.True(t, res.Result.Valid)
}

// An Officer line already in the written fit is the pilot's own: the rank counts only the
// Officer lines the solver introduces, so a Downgrade or Convert does not trade the owned
// module away to lower the count. The warning still names every Officer line of the result.
func TestSolve_RankCountsIntroducedOfficersOnly(t *testing.T) {
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 3, 50, rare(map[int]float64{})), // unusable for the poor pilot
		gun(1002, 1, 4, 45, nil),
		gun(1003, 5, 11, 60, nil), // Officer
	)
	c := NewCatalog(sub)
	seenOwned, seenIntroduced := false, false
	var probes []*probe
	h := &hooks{probe: func(p *probe) { probes = append(probes, p) }}
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		probes = nil
		r := Request{Kind: kind, Fit: highFit(1003, 1001, 1001), Skills: poor, Policy: fixPolicy(nil)}
		res, err := solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: 1}, r, h)
		require.NoError(t, err)
		require.Equal(t, 1003, highTypes(res.Fit)[0], "%s keeps the owned Officer line", kind)
		for _, p := range probes {
			want := 0
			for i, cand := range p.asg {
				if cand.TypeID == 1003 && i != 0 {
					want++
				}
			}
			require.Equal(t, want, p.rank.Officers, "%s %v", kind, p.asg)
			if p.asg[0].TypeID == 1003 {
				seenOwned = true
			}
			if want > 0 {
				seenIntroduced = true
			}
		}
		// The warning covers the owned Officer line (index 0) whatever the rank counted.
		require.NotEmpty(t, res.Warnings, kind)
		require.Equal(t, WarnOfficer, res.Warnings[0].Code)
		require.Equal(t, 0, res.Warnings[0].Index)
	}
	require.True(t, seenOwned && seenIntroduced)
}

// Convert candidates: Officer types sort after every other type, whatever their powergrid
// distance, but an Officer original is an ordinary candidate (it ranks by distance). Abyssal
// types are never offered, among weapons or rigs.
func TestCandidates_ConvertOfficerLastAndAbyssalExcluded(t *testing.T) {
	sub := testSubset()
	sub.Types = append(sub.Types,
		withMeta(ty(310, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40, attrMetaLevel: 20}), 5, 301), // Officer at distance 0
		withMeta(ty(311, groupSized, 7, []int{12, 42}, map[int]float64{attrPGNeed: 40, attrMetaLevel: 9}), 15, 301), // Abyssal at distance 0
		withMeta(ty(404, groupRig, 7, []int{2663}, map[int]float64{attrRigSize: 2, attrMetaLevel: 9}), 15, 400),     // Abyssal rig of the target size
	)
	c := NewCatalog(sub)
	o := Options{Kind: KindConvert, TargetHull: hullBig, Skills: full, Policy: Fallback(KindConvert)}
	f := fit.Fit{HullID: hullSmall, High: []fit.FitModule{{TypeID: 300, Qty: 1}}}
	got := ids(c.Candidates(f, hi0, o))
	require.NotContains(t, got, 311, "Abyssal")
	require.Contains(t, got, 310)
	require.Equal(t, 310, got[len(got)-2], "Officer after every other type, the empty slot last")
	require.Equal(t, 0, got[len(got)-1])

	// An Officer original is not demoted: it wants 160 MW on the target, 310 draws 40, so it
	// ranks by distance, before the 10 MW type 300 and not after every other type.
	f.High[0].TypeID = 310
	got = ids(c.Candidates(f, hi0, o))
	require.Less(t, slices.Index(got, 310), slices.Index(got, 300), "%v", got)

	rf := fit.Fit{HullID: hullSmall, Rig: []fit.FitModule{{TypeID: 400, Qty: 1}}}
	o.Policy.AllowRigChanges = true
	require.NotContains(t, ids(c.Candidates(rf, LineRef{Slot: SlotRig}, o)), 404, "Abyssal rig on Convert")
}
