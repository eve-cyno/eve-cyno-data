package solve

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// Search tests (plan 2026-11-07-dp2-fit-l4-l6, T5.5). Most run on a synthetic subset with a
// fake engine whose numbers are sums of made-up attributes, so each test sets the exact
// trade-off it checks. The real-SDE tests run the gofa engine through EngineEvaluator.

// Attributes the fake engine reads.
const (
	attrFakeDPS   = 9001 // DPS a module or drone adds (per unit)
	attrFakeEHP   = 9002 // EHP: the hull's base, plus what a module adds (may be negative)
	attrFakeCap   = 9003 // capacitor: the hull's budget, a module's use
	attrFakeUnmod = 9004 // > 0: the module is not modelled on tank
	attrCPUOut    = 48
	attrShipBay   = 283
	attrShipBW    = 1271

	hullS     = 10
	groupGun  = 510
	groupPlt  = 329
	fakeWhat  = "fake: not modelled"
	effHigh   = 12
	effLow    = 11
	effLaunch = 40
)

// fakeEngine is a deterministic Evaluator over a subset store. DPS is the sum of attrFakeDPS
// over modules and drones (times quantity), EHP the hull's attrFakeEHP plus the modules', the
// capacitor is stable when the modules' attrFakeCap use is within the hull's budget. CPU and
// powergrid are the base loads (50, 30) against the hull's 48 and 11. Skills change nothing.
type fakeEngine struct {
	store   *dogma.Store
	workers int
	calls   atomic.Int64
}

func (e *fakeEngine) EvaluateBatch(ctx context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	w := max(e.workers, 1)
	var wg sync.WaitGroup
	var next atomic.Int64
	for k := 0; k < w; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(reqs) {
					return
				}
				out[i] = e.eval(reqs[i])
			}
		}()
	}
	wg.Wait()
	e.calls.Add(int64(len(reqs)))
	return out, ctx.Err()
}

func (e *fakeEngine) eval(r gofa.Request) Evaluation {
	f := r.Fit
	hull := e.store.GetDogma(f.HullID)
	if len(hull) == 0 {
		return Evaluation{Err: context.DeadlineExceeded}
	}
	var ev Evaluation
	res := &ev.Resources
	res.CPU.Cap, res.PG.Cap = hull[attrCPUOut], hull[attrPGOut]
	res.DroneBay.Cap, res.DroneBandwidth.Cap = hull[attrShipBay], hull[attrShipBW]
	dps, ehp, capUse := 0.0, hull[attrFakeEHP], 0.0
	flags := fit.BlockFlags{Estimated: []fit.EstimatedFlag{}, NotModelled: []fit.NotModelledFlag{}}
	var unmod []string
	for _, g := range []struct {
		slot string
		mods []fit.FitModule
	}{{"hi", f.High}, {"mid", f.Mid}, {"low", f.Low}, {"rig", f.Rig}, {"subsystem", f.Subsystem}} {
		for _, m := range g.mods {
			d := e.store.GetDogma(m.TypeID)
			for u := 0; u < max(m.Qty, 1); u++ {
				dps += d[attrFakeDPS]
				ehp += d[attrFakeEHP]
				capUse += d[attrFakeCap]
				res.CPU.Used += d[attrCPUNeed]
				res.PG.Used += d[attrPGNeed]
				res.Modules = append(res.Modules, fit.ModuleResources{Slot: g.slot, TypeID: m.TypeID, CPU: d[attrCPUNeed], PG: d[attrPGNeed]})
			}
			if d[attrFakeUnmod] > 0 && len(unmod) == 0 {
				unmod = append(unmod, fakeWhat)
				flags.NotModelled = append(flags.NotModelled, fit.NotModelledFlag{What: fakeWhat, Affects: []fit.StatBlock{fit.BlockTank}})
			}
		}
	}
	for _, m := range f.Drones {
		d := e.store.GetDogma(m.TypeID)
		dps += d[attrFakeDPS] * float64(m.Qty)
		res.DroneBay.Used += d[attrVolume] * float64(m.Qty)
	}
	ev.Stats.DPS.Theoretical = dps
	ev.Stats.Tank.TotalEHP = ehp
	ev.Stats.Capacitor.Stable = capUse <= hull[attrFakeCap]
	if !ev.Stats.Capacitor.Stable {
		ev.Stats.Capacitor.SecondsToEmpty = 1000 / (capUse - hull[attrFakeCap])
	}
	ev.Stats.Unmodelled = unmod
	ev.Stats.Flags = flags
	return ev
}

// gun is a high-slot launcher of the test family (parent 1000): meta group, meta level, DPS,
// and extra attributes.
func gun(id, metaGroup int, metaLevel, dps float64, extra map[int]float64) dogma.SubsetType {
	a := map[int]float64{attrMetaLevel: metaLevel, attrFakeDPS: dps, attrCPUNeed: 10, attrPGNeed: 10}
	for k, v := range extra {
		a[k] = v
	}
	t := ty(id, groupGun, 7, []int{effHigh, effLaunch}, a)
	parent := 1000
	if id == 1000 {
		parent = 0
	}
	return withMeta(t, metaGroup, parent)
}

// plate is a low-slot module of the plate family (parent 2000).
func plate(id, metaGroup int, metaLevel, ehp float64, extra map[int]float64) dogma.SubsetType {
	a := map[int]float64{attrMetaLevel: metaLevel, attrFakeEHP: ehp, attrCPUNeed: 5, attrPGNeed: 5}
	for k, v := range extra {
		a[k] = v
	}
	parent := 2000
	if id == 2000 {
		parent = 0
	}
	return withMeta(ty(id, groupPlt, 7, []int{effLow}, a), metaGroup, parent)
}

// rare marks a type as needing skillRare V (the poor pilot cannot use it).
func rare(m map[int]float64) map[int]float64 { return needs(skillRare, 5, m) }

// fakeSubset is a hull with 3 high, 2 mid, 2 low and 2 rig slots, 3 launcher and 3 turret
// hardpoints, CPU and PG 100, base EHP 1000, a capacitor budget of 100, plus the given types.
func fakeSubset(types ...dogma.SubsetType) *dogma.Subset {
	T := []dogma.SubsetType{
		skillT(skillGun), skillT(skillRare),
		ty(hullS, groupHull, 6, nil, map[int]float64{14: 3, 13: 2, 12: 2, 1137: 2, 102: 3, 101: 3,
			attrPGOut: 100, attrCPUOut: 100, 1132: 400, attrShipBay: 50, attrShipBW: 25, attrRigSize: 1,
			attrFakeEHP: 1000, attrFakeCap: 100}),
	}
	return &dogma.Subset{Format: dogma.SubsetFormat, Types: append(T, types...)}
}

func highFit(ids ...int) fit.Fit {
	f := fit.Fit{HullID: hullS, HullName: "hull"}
	for _, id := range ids {
		f.High = append(f.High, fit.FitModule{TypeID: id, Qty: 1, Name: "t" + strconv.Itoa(id)})
	}
	return f
}

func fakeSolve(t *testing.T, sub *dogma.Subset, r Request) *Result {
	t.Helper()
	c := NewCatalog(sub)
	res, err := Solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: 3}, r)
	require.NoError(t, err)
	return res
}

func highTypes(f *fit.Fit) []int {
	out := make([]int, len(f.High))
	for i, m := range f.High {
		out[i] = m.TypeID
	}
	return out
}

func gateIs(stat string, at float64) []Gate { return []Gate{{Stat: stat, AtLeast: at}} }

func fixPolicy(gates []Gate) Policy {
	return Policy{Gates: gates, Baseline: BaselineFlown, AllowT2toT1: true, From: FromRequest}
}

// The fallback policy of Fix: DPS and EHP at 98 % of B2. Two unusable Tech II launchers move
// to the Tech I variant that keeps 99 % of the DPS; the variant with full DPS but a large EHP
// loss fails the EHP gate.
func TestSolve_FixFallbackGates(t *testing.T) {
	types := []dogma.SubsetType{
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1002, 1, 3, 49.5, nil),
		gun(1003, 1, 4, 50, map[int]float64{attrFakeEHP: -100}),
	}
	f := highFit(1001, 1001)
	res := fakeSolve(t, fakeSubset(types...), Request{Kind: KindRefit, Fit: f, Skills: poor})
	require.Equal(t, Fallback(KindRefit), res.Policy, "an empty policy is the fallback")
	require.Equal(t, StatusOKWithDowngrades, res.Status)
	require.Equal(t, []int{1002, 1002}, highTypes(res.Fit))
	require.Len(t, res.Changes, 2)
	for _, ch := range res.Changes {
		require.True(t, ch.Downgrade, "meta level 3 below 5")
		require.Equal(t, 1001, ch.From)
		require.Equal(t, 1002, ch.To)
	}
	require.InDelta(t, 100, res.Baseline.Flown.DPS, 1e-9)
	require.InDelta(t, 99, res.Result.DPS, 1e-9)
	require.True(t, res.Result.Valid)
	require.False(t, res.Baseline.Flown.Valid, "B2 is the original under the pilot, valid or not")
	require.True(t, res.Baseline.Designed.Valid)
	require.False(t, res.Capped)
	require.Nil(t, res.NoCandidate)

	// The 99 % variant now keeps only 97 %: nothing passes the 98 % DPS gate, so the result is
	// no_candidate with the closest valid fits.
	types[2] = gun(1002, 1, 3, 48.5, nil)
	res = fakeSolve(t, fakeSubset(types...), Request{Kind: KindRefit, Fit: f, Skills: poor})
	require.Equal(t, StatusNoCandidate, res.Status)
	require.Nil(t, res.Fit)
	require.Empty(t, res.Changes)
	require.NotNil(t, res.NoCandidate)
	require.NotEmpty(t, res.NoCandidate.Closest)
	require.LessOrEqual(t, len(res.NoCandidate.Closest), 2)
	require.Equal(t, []int{1002, 1002}, highTypes(&res.NoCandidate.Closest[0].Fit), "97 % DPS, full EHP: nearest the gates")
	for _, o := range res.NoCandidate.Closest {
		require.True(t, o.Result.Valid)
	}
	// Candidates that pass the gates but are invalid are kept for the binding (T5.7): here the
	// single changes that leave the other unusable launcher in place.
	require.NotEmpty(t, res.NoCandidate.GatePassingInvalid)
	for _, o := range res.NoCandidate.GatePassingInvalid {
		require.False(t, o.Result.Valid)
		require.NotEmpty(t, o.Violations)
	}
}

// Gates per stat: a DPS-only or EHP-only gate, and two tolerances on the same stat that pick
// different variants.
func TestSolve_PolicyPerStatGates(t *testing.T) {
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1002, 1, 3, 49.5, map[int]float64{attrFakeEHP: -400}), // DPS 99 %, EHP 60 %
		gun(1003, 1, 4, 46, nil),                                  // DPS 92 %, EHP 100 %
	)
	f := highFit(1001)
	cases := []struct {
		name  string
		gates []Gate
		want  int
	}{
		{"dps 90: both pass, the higher objective wins", gateIs(StatDPS, 90), 1003},
		{"dps 95: only the 99 % variant passes", gateIs(StatDPS, 95), 1002},
		{"ehp 90: the EHP loss fails", gateIs(StatEHP, 90), 1003},
		{"ehp 50: both pass", gateIs(StatEHP, 50), 1003},
		{"no gates: report only, best objective", []Gate{}, 1003},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor, Policy: fixPolicy(c.gates)})
			require.Equal(t, StatusOKWithDowngrades, res.Status)
			require.Equal(t, []int{c.want}, highTypes(res.Fit))
		})
	}
	// DPS 93 % and EHP 99 %: the 92 % variant fails the first, the 99 % one the second, the
	// rest have less DPS: no_candidate.
	res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor, Policy: fixPolicy([]Gate{{StatDPS, 93}, {StatEHP, 99}})})
	require.Equal(t, StatusNoCandidate, res.Status)
	// Against the designed baseline (B1, all V) the same numbers hold here: the fake engine
	// ignores skills.
	p := fixPolicy(gateIs(StatDPS, 95))
	p.Baseline = BaselineDesigned
	res = fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor, Policy: p})
	require.Equal(t, BaselineDesigned, res.Policy.Baseline)
	require.Equal(t, []int{1002}, highTypes(res.Fit))
}

// The search never depends on the order of a line's candidates or on the worker count: the
// same request gives the same bytes. Candidates with equal numbers tie, and the tie order
// (type ids in line order) decides.
func TestSolve_TiesReproducible(t *testing.T) {
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1004, 1, 4, 50, nil),
		gun(1005, 1, 4, 50, nil),
		gun(1006, 1, 4, 50, nil),
		gun(1007, 1, 2, 45, nil),
		plate(2000, 1, 0, 100, nil),
		plate(2001, 2, 5, 150, rare(map[int]float64{})),
		plate(2002, 1, 4, 150, nil),
		plate(2003, 1, 4, 150, nil),
	)
	f := highFit(1001, 1001, 1001)
	f.Low = []fit.FitModule{{TypeID: 2001, Qty: 1}, {TypeID: 2001, Qty: 1}}
	c := NewCatalog(sub)
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		var first []byte
		for run := 0; run < 20; run++ {
			for _, w := range []int{1, 8} {
				rng := rand.New(rand.NewSource(int64(run*31 + w)))
				h := &hooks{shuffle: func(_ int, cs []Candidate) { rng.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] }) }}
				res, err := solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: w}, Request{Kind: kind, Fit: f, Skills: poor}, h)
				require.NoError(t, err)
				b, err := json.Marshal(res)
				require.NoError(t, err)
				if first == nil {
					first = b
					require.Equal(t, []int{1004, 1004, 1004}, highTypes(res.Fit), "%s: equal numbers, lowest type id", kind)
					require.Equal(t, 2002, res.Fit.Low[0].TypeID)
					continue
				}
				require.Equal(t, string(first), string(b), "%s run %d workers %d", kind, run, w)
			}
		}
	}

	// Real engine: Cerberus reference 3 under missiler (Fix) and the Vexor under droner
	// (Downgrade), workers 1 and 8, shuffled candidates.
	rc := realCatalog(t)
	eng := gofa.New(rc.Store())
	runs := 20
	if shrinkUnderRace() {
		runs = 2
	}
	profiles := e3aProfiles(t)
	for _, pc := range []struct {
		ref     int
		profile string
		kind    Kind
	}{{3, "missiler", KindRefit}, {2, "droner", KindDowngrade}} {
		f := refFit(t, rc, pc.ref)
		var first []byte
		for run := 0; run < runs; run++ {
			for _, w := range []int{1, 8} {
				rng := rand.New(rand.NewSource(int64(run*7 + w)))
				h := &hooks{shuffle: func(_ int, cs []Candidate) { rng.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] }) }}
				res, err := solve(context.Background(), rc, NewEngineEvaluator(eng, w), Request{Kind: pc.kind, Fit: f, Skills: profiles[pc.profile]}, h)
				require.NoError(t, err)
				require.False(t, res.Capped)
				b, err := json.Marshal(res)
				require.NoError(t, err)
				if first == nil {
					first = b
					t.Logf("ref %d %s %s: %s, %d changes, %d evaluations", pc.ref, pc.profile, pc.kind, res.Status, len(res.Changes), res.Evaluations)
					continue
				}
				require.Equal(t, string(first), string(b), "ref %d run %d workers %d", pc.ref, run, w)
			}
		}
	}
}

// No candidate fit holds a type outside the subset or an unpublished one, unless it is the
// original line's own type.
func TestSolve_NeverOutsideSubset(t *testing.T) {
	skipHeavyRace(t)
	// Synthetic: a line whose type the subset does not hold is dropped, a charge it does not
	// hold is unloaded.
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1002, 1, 3, 49.5, nil),
		withMeta(unpub(gun(1008, 2, 5, 60, nil)), 2, 1000),
	)
	f := highFit(1001, 7777, 1000)
	f.High[2].ChargeID = 8888
	res := fakeSolve(t, sub, Request{Kind: KindDowngrade, Fit: f, Skills: poor})
	require.NotEqual(t, StatusNoCandidate, res.Status)
	c := NewCatalog(sub)
	assertInSubset(t, c, f, res)
	require.NotContains(t, highTypes(res.Fit), 7777)
	require.NotContains(t, highTypes(res.Fit), 1008, "unpublished variant")
	for _, m := range res.Fit.High {
		require.NotEqual(t, 8888, m.ChargeID)
	}
	// A start whose other lines are valid: the type outside the subset still never comes back.
	for _, k := range []Kind{KindRefit, KindDowngrade} {
		r2 := fakeSolve(t, fakeSubset(gun(1000, 1, 0, 40, nil), gun(1002, 1, 3, 49.5, nil)),
			Request{Kind: k, Fit: highFit(1000, 7777), Skills: poor})
		if r2.Fit != nil {
			require.NotContains(t, highTypes(r2.Fit), 7777, k)
		}
	}

	// Real SDE: the reference fits under two profiles, Fix and Downgrade.
	rc := realCatalog(t)
	eng := gofa.New(rc.Store())
	profiles := e3aProfiles(t)
	refs := []int{1, 2, 3, 5, 6}
	names := []string{"fitIII", "droner"}
	if testing.Short() {
		refs, names = []int{1, 3}, []string{"fitIII"}
	}
	for _, ref := range refs {
		f := refFit(t, rc, ref)
		for _, p := range names {
			for _, kind := range []Kind{KindRefit, KindDowngrade} {
				res, err := Solve(context.Background(), rc, NewEngineEvaluator(eng, 4), Request{Kind: kind, Fit: f, Skills: profiles[p]})
				require.NoError(t, err)
				t.Logf("ref %d %s %s: %s, %d changes, %d evaluations, capped %v", ref, p, kind, res.Status, len(res.Changes), res.Evaluations, res.Capped)
				assertInSubset(t, rc, f, res)
			}
		}
	}
}

// assertInSubset checks every fit of a result: each module, charge and drone is in the subset
// and published, or is the original line's own type or charge.
func assertInSubset(t *testing.T, c *Catalog, orig fit.Fit, res *Result) {
	t.Helper()
	own := map[int]bool{}
	for _, g := range [][]fit.FitModule{orig.High, orig.Mid, orig.Low, orig.Rig, orig.Subsystem, orig.Drones} {
		for _, m := range g {
			if c.Has(m.TypeID) {
				own[m.TypeID] = true
			}
			if c.Has(m.ChargeID) {
				own[m.ChargeID] = true
			}
		}
	}
	check := func(f *fit.Fit) {
		for _, g := range [][]fit.FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem, f.Drones} {
			for _, m := range g {
				for _, id := range []int{m.TypeID, m.ChargeID} {
					if id == 0 || own[id] {
						continue
					}
					require.True(t, c.Has(id), "type %d is in the subset", id)
					require.True(t, c.Published(id), "type %d is published", id)
				}
			}
		}
	}
	if res.Fit != nil {
		check(res.Fit)
	}
	if nc := res.NoCandidate; nc != nil {
		for _, o := range append(append([]Option(nil), nc.Closest...), nc.GatePassingInvalid...) {
			check(&o.Fit)
		}
	}
}

// A chosen fit with a not-modelled entry on dps, tank or capacitor is uncertain, never ok.
func TestSolve_UncertainWhenNotModelled(t *testing.T) {
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1002, 1, 3, 49.5, nil),
		plate(2000, 1, 0, 100, map[int]float64{attrFakeUnmod: 1}),
	)
	f := highFit(1000)
	f.Low = []fit.FitModule{{TypeID: 2000, Qty: 1}}
	res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: full})
	require.Equal(t, StatusUncertain, res.Status, "nothing changes, but the plate is not modelled")
	require.Equal(t, []string{fakeWhat}, res.Uncertain)
	require.Empty(t, res.Changes)
	// With a downgrade it stays uncertain; the change keeps its downgrade flag.
	f.High[0].TypeID = 1001
	res = fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor})
	require.Equal(t, StatusUncertain, res.Status)
	require.True(t, res.Changes[0].Downgrade)
	// Without the flag: ok.
	f.Low = nil
	f.High[0].TypeID = 1000
	res = fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: full})
	require.Equal(t, StatusOK, res.Status)
	require.Empty(t, res.Uncertain)

	// Real engine: an ancillary armor repairer runs on charges, so the capacitor and tank are
	// not fully modelled (gofa.UnmodelledChargeFedCap).
	rc := realCatalog(t)
	f = refFit(t, rc, 2)
	anc := rc.Store().ResolveNames([]string{"Medium Ancillary Armor Repairer", "Medium Armor Repairer II"})
	require.Len(t, anc, 2)
	for i, m := range f.Low {
		if m.TypeID == anc["Medium Armor Repairer II"] {
			f.Low[i].TypeID, f.Low[i].Name = anc["Medium Ancillary Armor Repairer"], "Medium Ancillary Armor Repairer"
		}
	}
	res, err := Solve(context.Background(), rc, NewEngineEvaluator(gofa.New(rc.Store()), 2), Request{Kind: KindRefit, Fit: f})
	require.NoError(t, err)
	require.Equal(t, StatusUncertain, res.Status)
	require.Contains(t, res.Uncertain, gofa.UnmodelledChargeFedCap)
}

// Owner ruling 2026-11-09 (4a): a replacement may make the capacitor worse than the flown fit.
// The cap-unstable variant with the best objective wins over the stable one.
func TestSolve_CapWorseIsAllowed(t *testing.T) {
	types := []dogma.SubsetType{
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{attrFakeCap: 30})),
		gun(1002, 1, 4, 50, map[int]float64{attrFakeCap: 120}), // best DPS, cap unstable
		gun(1003, 1, 3, 48, map[int]float64{attrFakeCap: 20}),
	}
	f := highFit(1001)
	r := Request{Kind: KindRefit, Fit: f, Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 90))}
	res := fakeSolve(t, fakeSubset(types...), r)
	require.True(t, res.Baseline.Flown.CapStable)
	require.Equal(t, []int{1002}, highTypes(res.Fit))
	require.False(t, res.Result.CapStable)
	// The worse capacitor is warned (the fake store has no skill that moves it: no train).
	require.Len(t, res.Warnings, 1)
	w := res.Warnings[0]
	require.Equal(t, WarnCapacitor, w.Code)
	require.Equal(t, "", w.Group)
	require.Regexp(t, `^The capacitor runs empty in \d+ s\. The original fit is stable\.$`, w.Message)
	require.Empty(t, w.Train)

	// B2 unstable (the original uses 150): the same pick.
	types[1] = gun(1001, 2, 5, 50, rare(map[int]float64{attrFakeCap: 150}))
	res = fakeSolve(t, fakeSubset(types...), r)
	require.False(t, res.Baseline.Flown.CapStable)
	require.Equal(t, []int{1002}, highTypes(res.Fit))
	require.Empty(t, res.Warnings, "no warning when the flown fit was not stable either")

	// The same variant with a stable capacitor draws no warning.
	types[2] = gun(1002, 1, 4, 50, map[int]float64{attrFakeCap: 20})
	types[1] = gun(1001, 2, 5, 50, rare(map[int]float64{attrFakeCap: 30}))
	res = fakeSolve(t, fakeSubset(types...), r)
	require.True(t, res.Result.CapStable)
	require.Empty(t, res.Warnings)
}

// Owner ruling 2026-11-09 (4b): an Officer module is warned, per line, also when the written
// line was already Officer; it is chosen only when viability or a gate needs it.
func TestSolve_OfficerWarnedAndAvoided(t *testing.T) {
	types := func(normalDPS float64) []dogma.SubsetType {
		return []dogma.SubsetType{
			gun(1000, 1, 0, 40, nil),
			gun(1001, 2, 3, 50, rare(map[int]float64{})),  // the written line: the poor pilot cannot use it
			gun(1002, 1, 4, normalDPS, nil),               // usable, level above the original: no downgrade
			gun(1003, 5, 11, 60, nil),                     // Officer, best DPS
			gun(1004, 5, 12, 70, rare(map[int]float64{})), // Officer the poor pilot cannot use
		}
	}
	r := Request{Kind: KindRefit, Fit: highFit(1001), Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 90))}

	// The normal variant passes the gate (48 of 50): the Officer is not chosen, whatever its DPS.
	res := fakeSolve(t, fakeSubset(types(48)...), r)
	require.Equal(t, []int{1002}, highTypes(res.Fit))
	require.Empty(t, res.Warnings)

	// The normal variant fails the gate (40 of 50): the Officer is needed, and warned.
	res = fakeSolve(t, fakeSubset(types(40)...), r)
	require.Equal(t, []int{1003}, highTypes(res.Fit))
	require.Len(t, res.Warnings, 1)
	w := res.Warnings[0]
	require.Equal(t, Warning{Code: WarnOfficer, Group: SlotHigh, Index: 0, TypeID: 1003, Message: w.Message}, w)
	require.Equal(t, "tP is an Officer module. Officer modules cost far more than the other variants.", w.Message)

	// An Officer line already in the written fit is kept and warned, one warning per line, in
	// line order.
	r2 := Request{Kind: KindRefit, Fit: highFit(1003, 1000, 1003), Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 90))}
	res = fakeSolve(t, fakeSubset(types(48)...), r2)
	require.Empty(t, res.Changes)
	require.Len(t, res.Warnings, 2)
	require.Equal(t, 0, res.Warnings[0].Index)
	require.Equal(t, 2, res.Warnings[1].Index)
	require.Equal(t, 1003, res.Warnings[1].TypeID)

	// No candidate: an empty list, not null.
	r3 := Request{Kind: KindRefit, Fit: highFit(1001), Skills: poor, Policy: fixPolicy(gateIs(StatDPS, 400))}
	res = fakeSolve(t, fakeSubset(types(48)...), r3)
	require.Equal(t, StatusNoCandidate, res.Status)
	b, err := json.Marshal(res)
	require.NoError(t, err)
	require.Contains(t, string(b), `"warnings":[]`)
}

// When the cap ends the search, the result is capped and carries the best gate-passing fit
// found; with none, no_candidate.
func TestSolve_CappedReportsBest(t *testing.T) {
	sub := fakeSubset(
		gun(1000, 1, 0, 40, nil),
		gun(1001, 2, 5, 50, rare(map[int]float64{})),
		gun(1002, 1, 3, 49.5, nil),
		gun(1003, 1, 4, 49, nil),
		gun(1007, 1, 2, 45, nil),
		plate(2000, 1, 0, 100, nil),
		plate(2001, 2, 5, 150, rare(map[int]float64{})),
		plate(2002, 1, 4, 150, nil),
	)
	f := highFit(1001, 1001, 1001)
	f.Low = []fit.FitModule{{TypeID: 2001, Qty: 1}, {TypeID: 2001, Qty: 1}}
	c := NewCatalog(sub)
	run := func(limit int) (*Result, []*probe) {
		var seen []*probe
		h := &hooks{probe: func(p *probe) { seen = append(seen, p) }}
		res, err := solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: 2},
			Request{Kind: KindDowngrade, Fit: f, Skills: poor, MaxEvaluations: limit}, h)
		require.NoError(t, err)
		return res, seen
	}
	full, _ := run(0)
	require.False(t, full.Capped)
	require.Equal(t, StatusOKWithDowngrades, full.Status)
	require.Less(t, full.Evaluations, CapInteractive)

	// Only the baselines fit under the cap: no candidate was evaluated.
	res, _ := run(2)
	require.True(t, res.Capped)
	require.Equal(t, 2, res.Evaluations)
	require.Equal(t, StatusNoCandidate, res.Status)

	// Every cap between: capped, never above the cap, and the best accepted fit seen.
	for limit := 3; limit < full.Evaluations; limit++ {
		res, seen := run(limit)
		require.True(t, res.Capped, "cap %d", limit)
		require.LessOrEqual(t, res.Evaluations, limit)
		var best *probe
		s := &search{kind: KindDowngrade}
		for _, p := range seen {
			if p.accepted() && (best == nil || s.better(p, best)) {
				best = p
			}
		}
		if best == nil {
			require.Equal(t, StatusNoCandidate, res.Status, "cap %d", limit)
			continue
		}
		require.NotEqual(t, StatusNoCandidate, res.Status, "cap %d", limit)
		require.Equal(t, best.rank.TypeIDs, rankTypes(res), "cap %d: the best accepted fit seen", limit)
	}
}

// rankTypes lists the result fit's type ids in line order (high, low here).
func rankTypes(res *Result) []int {
	var out []int
	for _, m := range res.Fit.High {
		out = append(out, m.TypeID)
	}
	for _, m := range res.Fit.Low {
		out = append(out, m.TypeID)
	}
	return out
}

// A fit with no DPS (logistics) ranks by EHP alone: the DPS ratio is left out of the
// objective and the DPS gate is met.
func TestSolve_ZeroBaselineStat(t *testing.T) {
	sub := fakeSubset(
		plate(2000, 1, 0, 100, nil),
		plate(2001, 2, 5, 150, rare(map[int]float64{})),
		plate(2002, 1, 4, 140, nil),
		plate(2003, 1, 3, 145, map[int]float64{attrPGNeed: 6}),
	)
	f := fit.Fit{HullID: hullS, Low: []fit.FitModule{{TypeID: 2001, Qty: 1}, {TypeID: 2001, Qty: 1}}}
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		res := fakeSolve(t, sub, Request{Kind: kind, Fit: f, Skills: poor})
		require.Equal(t, 0.0, res.Baseline.Flown.DPS)
		require.Equal(t, StatusOKWithDowngrades, res.Status, string(kind))
		require.Equal(t, 0.0, res.Result.DPS)
		for _, m := range res.Fit.Low {
			require.Equal(t, 2003, m.TypeID, "%s: highest EHP (meta level 3 beats 4 on objective)", kind)
		}
	}
	// EHP gate at 99.5 %: the best fit has 1,290 of 1,300 (99.2 %), so nothing passes; the DPS
	// gate (baseline 0) is met.
	res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor, Policy: fixPolicy([]Gate{{StatDPS, 98}, {StatEHP, 99.5}})})
	require.Equal(t, StatusNoCandidate, res.Status)
}

// Repair (Downgrade): each forced pick fits on its own, together they overrun the powergrid.
// Repair first takes a change that clears the violation, then the change that saves the most
// MW (fewest changed lines, decision 2026-11-09), then the least objective loss per MW saved,
// then the tie order.
func TestSolve_RepairLowersBinding(t *testing.T) {
	scenario := func(pg1000, pg1001 float64) []int {
		sub := fakeSubset(
			gun(1000, 1, 0, 30, map[int]float64{attrPGNeed: pg1000}),
			gun(1001, 2, 5, 50, rare(map[int]float64{attrPGNeed: pg1001})),
			gun(1002, 1, 4, 50, map[int]float64{attrPGNeed: 40}), // best DPS, heavy
			gun(1003, 1, 3, 45, map[int]float64{attrPGNeed: 25}),
		)
		res := fakeSolve(t, sub, Request{Kind: KindDowngrade, Fit: highFit(1001, 1001, 1001), Skills: poor})
		require.Equal(t, StatusOKWithDowngrades, res.Status)
		require.True(t, res.Result.Valid)
		return highTypes(res.Fit)
	}
	// 3 x 1002 = 120 MW of 100. 1000 (-20 MW) clears it on one line; 1003 (-15) does not.
	// Cleared comes first; the three lines tie, and type ids in line order put 1000 first.
	require.Equal(t, []int{1000, 1002, 1002}, scenario(20, 30))
	// 1000 now draws 22 MW: no single change clears the 20 MW. 1000 saves 18 MW (loses 20
	// DPS), 1003 saves 15 MW (loses 5 DPS): the larger saving comes first (on the first
	// line: it sorts before the current type), then a 1003 on the last line clears the rest; improve
	// then trades the changed first line for 1003 (more DPS). Two changed lines, as before.
	require.Equal(t, []int{1003, 1002, 1003}, scenario(22, 20))
}

// A hull the pilot cannot fly ends at once: no_candidate with the hull's missing skills.
func TestSolve_HullUnusable(t *testing.T) {
	sub := fakeSubset(gun(1000, 1, 0, 40, nil))
	for i := range sub.Types {
		if sub.Types[i].ID == hullS {
			sub.Types[i].Attrs = needs(skillRare, 3, sub.Types[i].Attrs)
		}
	}
	res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: highFit(1000), Skills: poor})
	require.Equal(t, StatusNoCandidate, res.Status)
	require.NotNil(t, res.NoCandidate.Hull)
	require.Equal(t, hullS, res.NoCandidate.Hull.TypeID)
	require.Equal(t, []fit.MissingSkill{{TypeID: skillRare, Needs: 3, Has: 0}}, res.NoCandidate.Hull.Missing)
	require.LessOrEqual(t, res.Evaluations, 2, "baselines only")
}

func TestSolve_BadRequest(t *testing.T) {
	c := NewCatalog(fakeSubset(gun(1000, 1, 0, 40, nil)))
	ev := &fakeEngine{store: c.Store()}
	for name, r := range map[string]Request{
		"unknown hull":    {Kind: KindRefit, Fit: fit.Fit{HullID: 4242}},
		"unknown kind":    {Kind: "fix", Fit: highFit(1000)},
		"unknown gate":    {Kind: KindRefit, Fit: highFit(1000), Policy: fixPolicy(gateIs("speed", 90))},
		"bad baseline":    {Kind: KindRefit, Fit: highFit(1000), Policy: Policy{Baseline: "best", From: FromRequest}},
		"convert no hull": {Kind: KindConvert, Fit: highFit(1000)},
	} {
		_, err := Solve(context.Background(), c, ev, r)
		require.Error(t, err, name)
	}
}

// The data default evaluator: Stats and Resources pruned equal the full engine's.
func TestEngineEvaluator_EqualsFull(t *testing.T) {
	rc := realCatalog(t)
	eng := gofa.New(rc.Store())
	profiles := e3aProfiles(t)
	var reqs []gofa.Request
	for ref := 1; ref <= 6; ref++ {
		f := refFit(t, rc, ref)
		for _, p := range []string{"all0", "fitIII", "droner"} {
			reqs = append(reqs, gofa.Request{Fit: f, Opts: fit.StatsOpts{Skills: profiles[p], MissingAsZero: true}})
		}
	}
	got, err := NewEngineEvaluator(eng, 4).EvaluateBatch(context.Background(), reqs)
	require.NoError(t, err)
	require.Len(t, got, len(reqs))
	for i, r := range reqs {
		st, err := eng.Stats(context.Background(), r.Fit, r.Opts)
		require.NoError(t, err)
		res, err := eng.Resources(context.Background(), r.Fit, r.Opts)
		require.NoError(t, err)
		require.NoError(t, got[i].Err)
		require.Equal(t, st, got[i].Stats, "request %d stats", i)
		require.Equal(t, res, got[i].Resources, "request %d resources", i)
	}
}

// Real-SDE fixtures, shared by the package's tests.

var (
	realOnce sync.Once
	realCat  *Catalog
	realSub  *dogma.Subset
	realErr  error

	t12Once sync.Once
	t12Cat  *Catalog
)

// realCatalog is the catalog over the subset of the pinned SDE, built once per test binary.
func realCatalog(t testing.TB) *Catalog {
	t.Helper()
	p := sdetest.Path(t)
	realOnce.Do(func() {
		s, err := realsde.Open(p)
		if err != nil {
			realErr = err
			return
		}
		defer s.Close()
		if !s.Available() {
			realErr = os.ErrNotExist
			return
		}
		sub, err := s.ExportSubset("test")
		if err != nil {
			realErr = err
			return
		}
		realSub = sub
		realCat = NewCatalog(sub)
	})
	if realErr != nil {
		sdetest.Skip(t, "real SDE not usable: "+realErr.Error())
	}
	return realCat
}

// refFit parses reference fit n (fit/testdata/e3a/refN.eft) over the catalog's store.
func refFit(t testing.TB, c *Catalog, n int) fit.Fit {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "e3a", "ref"+strconv.Itoa(n)+".eft"))
	require.NoError(t, err)
	f, unresolved := fit.ParseEFT(string(raw), c.Store())
	require.Empty(t, unresolved, "ref%d", n)
	return f
}

// e3aProfiles reads the E3a skill profiles (fit/testdata/e3a/profiles.json).
func e3aProfiles(t testing.TB) map[string]fit.SkillProfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "e3a", "profiles.json"))
	require.NoError(t, err)
	var file struct {
		Profiles map[string]struct {
			Skills map[string]int `json:"skills"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	out := map[string]fit.SkillProfile{}
	for id, p := range file.Profiles {
		sp := fit.SkillProfile{}
		for k, v := range p.Skills {
			n, err := strconv.Atoi(k)
			require.NoError(t, err)
			sp[n] = v
		}
		out[id] = sp
	}
	return out
}

// techCatalog is the real catalog without the modules of any meta group but Tech I and Tech II:
// the candidate universe of the rule before the 2026-11-09 rulings. The report tests use it for
// no_candidate scenarios that the wider candidate set now solves. Charges, drones and hulls are
// kept.
func techCatalog(t testing.TB) *Catalog {
	t.Helper()
	realCatalog(t)
	t12Once.Do(func() {
		sub := &dogma.Subset{Format: realSub.Format, SDEVersion: realSub.SDEVersion, Effects: realSub.Effects, Attributes: realSub.Attributes}
		for _, ty := range realSub.Types {
			if ty.Category == 7 && ty.MetaGroup > 2 {
				continue
			}
			sub.Types = append(sub.Types, ty)
		}
		t12Cat = NewCatalog(sub)
	})
	return t12Cat
}

// Interchangeable lines (same group, written module and candidates) are evaluated once per
// round of repair and improve. The result equals the one with every line evaluated, and the
// evaluations fall.
// The twin rule skips evaluations, never a successful result: the results are byte-identical
// with and without it (TestSolve_TwinSkippingKeepsSuccessfulResults runs random universes). A
// no_candidate report (Closest, GatePassingInvalid) may differ, because it lists the evaluated
// candidates and a skipped twin is not evaluated; this test therefore compares successes only.
func TestSolve_InterchangeableLinesEvaluatedOnce(t *testing.T) {
	cpu := func(v float64) map[int]float64 { return map[int]float64{attrCPUNeed: v} }
	sub := fakeSubset(
		gun(1000, 1, 0, 10, cpu(50)),
		gun(1001, 2, 5, 50, cpu(50)),
		gun(1002, 1, 4, 45, cpu(40)),
		gun(1003, 1, 3, 40, cpu(30)),
		gun(1004, 1, 2, 30, cpu(20)),
	)
	c := NewCatalog(sub)
	f := highFit(1001, 1001, 1001) // 150 CPU of 100
	for _, kind := range []Kind{KindRefit, KindDowngrade} {
		r := Request{Kind: kind, Fit: f, Skills: poor, Policy: fixPolicy(nil)}
		run := func(h *hooks) *Result {
			res, err := solve(context.Background(), c, &fakeEngine{store: c.Store(), workers: 2}, r, h)
			require.NoError(t, err)
			return res
		}
		with, without := run(nil), run(&hooks{noSym: true})
		require.NotEqual(t, StatusNoCandidate, with.Status)
		require.True(t, with.Result.Valid)
		require.Less(t, with.Evaluations, without.Evaluations, "%s", kind)
		t.Logf("%s: %d evaluations, %d without the twin rule", kind, with.Evaluations, without.Evaluations)
		with.Evaluations = without.Evaluations
		a, _ := json.Marshal(with)
		b, _ := json.Marshal(without)
		require.Equal(t, string(b), string(a), kind)
	}
}

// Review (fit parity, Opus): an unmodelled module effect is one line, not its flag and its guard entry.
func TestUncertainOf_ModuleEffectIsNotListedTwice(t *testing.T) {
	st := fit.FitStats{Unmodelled: []string{"effect:38"}}
	st.Flags.NotModelled = []fit.NotModelledFlag{{What: "Module effect 38", Affects: []fit.StatBlock{fit.BlockDPS}}}
	require.Equal(t, []string{"Module effect 38"}, uncertainOf(st))
}

// The level-I floor of an absent hull skill is a real character's: the request says so (AbsentHullSkillAtI). An "all skills 0"
// pilot (an empty, non-nil map and no floor) stays every skill at 0, as evaluate does (review finding M1 of the allZero change).
func TestSetup_AbsentHullSkillAtIFollowsTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		skills fit.SkillProfile
		floor  bool
		want   bool
	}{
		{"all skills 0: empty profile, no floor", fit.SkillProfile{}, false, false},
		{"a real character: empty profile with the floor", fit.SkillProfile{}, true, true},
		{"all V: a nil profile never has it", nil, true, false},
	} {
		sub := fakeSubset(plate(2000, 1, 0, 100, nil))
		c := NewCatalog(sub)
		s := &search{cat: c, ev: &fakeEngine{store: c.Store(), workers: 1}, req: Request{Kind: KindRefit, Fit: fit.Fit{HullID: hullS}, Skills: tc.skills, AbsentHullSkillAtI: tc.floor}, kind: KindRefit}
		require.NoError(t, s.setup(), tc.name)
		require.Equal(t, tc.want, s.pilot.AbsentHullSkillAtI, tc.name)
		require.Equal(t, tc.skills != nil, s.pilot.MissingAsZero, tc.name)
	}
}
