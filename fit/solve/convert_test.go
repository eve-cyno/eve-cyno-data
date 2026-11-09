package solve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// Convert tests (plan 2026-11-07-dp2-fit-l4-l6, T5.6), real SDE through EngineEvaluator.

// Hulls of the pinned build (SDE attributes 14/13/12/1137 slots, 102/101 hardpoints, 1547
// rig size, 283/1271 drone bay and bandwidth, read 2026-11-09):
const (
	hullRifter    = 587   // 3/3/4 slots, 3 turrets, 2 launchers, rig size 1
	hullTristan   = 593   // 3/3/3 slots, 2 turrets, rig size 1
	hullStabber   = 622   // 6/4/4 slots, 4 turrets, 2 launchers, rig size 2
	hullThorax    = 627   // drone bay 50, bandwidth 50
	hullDrake     = 24698 // 7/6/4 slots, 6 launchers
	hullTengu     = 29984 // T3: slots from subsystems
	hullLoki      = 29990 // another T3
	hullCerberus  = 11993
	hullMegathron = 641   // 7 turrets, 8 low
	hullHyperion  = 24690 // 7 high, 6 turrets, 7 low
)

func convertSolve(t *testing.T, c *Catalog, f fit.Fit, target int, skills fit.SkillProfile) *Result {
	t.Helper()
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4),
		Request{Kind: KindConvert, Fit: f, TargetHull: target, Skills: skills})
	require.NoError(t, err)
	t.Logf("%s -> %d: %s, %d changes, %d evaluations, capped %v", f.HullName, target, res.Status, len(res.Changes), res.Evaluations, res.Capped)
	return res
}

func countEffect(c *Catalog, mods []fit.FitModule, effect int) int {
	n := 0
	for _, m := range mods {
		if t := c.types[m.TypeID]; t != nil && hasEffect(t, effect) {
			n += max(m.Qty, 1)
		}
	}
	return n
}

// Converting a fit to its own hull under all V gives the original with 0 changes.
func TestConvert_OwnHullNoChange(t *testing.T) {
	c := realCatalog(t)
	for ref := 1; ref <= 6; ref++ {
		f := refFit(t, c, ref)
		res := convertSolve(t, c, f, f.HullID, nil)
		require.Equal(t, StatusOK, res.Status, "ref %d", ref)
		require.Empty(t, res.Changes, "ref %d", ref)
		require.Equal(t, f, *res.Fit, "ref %d", ref)
		require.Equal(t, res.Baseline.Flown, *res.Result, "ref %d: the result is B2", ref)
		require.Equal(t, 1, res.Evaluations, "ref %d: all V, so B1 and B2 are one request, and the start is B2", ref)
	}
}

// Weapons are capped by the target's hardpoints and lines by its slots; the search drops
// the line whose loss is least.
func TestConvert_HardpointsCap(t *testing.T) {
	c := realCatalog(t)
	for _, pc := range []struct {
		ref, target       int
		turrets, lowSlots int
	}{
		{1, hullTristan, 2, 3},  // 3 autocannons, 4 lows
		{5, hullHyperion, 6, 7}, // 7 blasters, 8 lows
	} {
		f := refFit(t, c, pc.ref)
		require.Greater(t, countEffect(c, f.High, effectTurretFitted), pc.turrets)
		require.Greater(t, len(f.Low), pc.lowSlots)
		res := convertSolve(t, c, f, pc.target, nil)
		require.Contains(t, []Status{StatusOK, StatusOKWithDowngrades}, res.Status)
		require.True(t, res.Result.Valid)
		require.LessOrEqual(t, countEffect(c, res.Fit.High, effectTurretFitted), pc.turrets)
		require.LessOrEqual(t, len(res.Fit.Low), pc.lowSlots)
		require.Equal(t, pc.turrets, countEffect(c, res.Fit.High, effectTurretFitted), "only what the hardpoints force is dropped")
		require.Equal(t, pc.target, res.Fit.HullID)
		for _, g := range slotGroups {
			for i, m := range *g.mods(res.Fit) {
				if m.ChargeID == 0 {
					require.Empty(t, m.Charge, "%s[%d]: no charge has no name (the SDE's type 0 is #System)", g.slot, i)
				}
			}
		}
		dropped := 0
		for _, ch := range res.Changes {
			if ch.To == 0 {
				dropped++
				require.False(t, ch.Downgrade, "dropping a usable line is not a downgrade")
			}
		}
		require.GreaterOrEqual(t, dropped, 2)
	}
}

// A kept module whose charge has no sibling of the new size is unloaded: no charge id and
// no charge name (the SDE's type 0 is "#System").
func TestConvert_UnloadedChargeHasNoName(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 1)
	m := f.High[0]
	require.NotZero(t, m.ChargeID)
	s := &search{cat: c, req: Request{Kind: KindConvert, Fit: f}, hull: f.HullID, hullName: f.HullName}
	s.lines = []line{{ref: LineRef{Slot: SlotHigh}, mod: m, orig: Candidate{TypeID: m.TypeID, ChargeID: m.ChargeID, Qty: m.Qty}}}
	got := s.buildFit([]Candidate{{TypeID: m.TypeID, Qty: m.Qty}})
	require.Len(t, got.High, 1)
	require.Equal(t, m.TypeID, got.High[0].TypeID)
	require.Zero(t, got.High[0].ChargeID)
	require.Empty(t, got.High[0].Charge)
}

// Rigs map to the member of their family whose rig size equals the target's.
func TestConvert_RigSizeFamily(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 1) // two Small Projectile Burst Aerator I
	src := c.types[f.Rig[0].TypeID]
	want := c.types[hullStabber].attrs[attrRigSize]

	// The mapping: every rig line offers only medium members of its family.
	s := &search{ctx: context.Background(), cat: c, req: Request{Kind: KindConvert, Fit: f, TargetHull: hullStabber},
		kind: KindConvert, pol: Fallback(KindConvert), hull: hullStabber, canUse: map[int]bool{}, down: map[[2]int]bool{}}
	nc, err := s.convertLines()
	require.NoError(t, err)
	require.Nil(t, nc)
	rigs := 0
	for _, ln := range s.lines {
		if ln.ref.Slot != SlotRig {
			continue
		}
		rigs++
		n := 0
		for _, cd := range ln.cands {
			if cd.Empty() {
				continue
			}
			n++
			got := c.types[cd.TypeID]
			require.Equal(t, want, got.attrs[attrRigSize], "rig %d candidate %d", ln.ref.Index, cd.TypeID)
			require.Equal(t, shapeKey(src), shapeKey(got), "rig %d candidate %d keeps the family (group and effects)", ln.ref.Index, cd.TypeID)
		}
		require.Positive(t, n, "rig %d has a size-2 sibling", ln.ref.Index)
	}
	require.Equal(t, len(f.Rig), rigs)

	// The solve keeps rigs of that family. Calibration may still drop one: the Stabber has
	// 400, the search takes Medium Projectile Burst Aerator II (300) on the first line and
	// a second rig (200 or 300) no longer fits.
	res := convertSolve(t, c, f, hullStabber, nil)
	require.Contains(t, []Status{StatusOK, StatusOKWithDowngrades}, res.Status)
	require.True(t, res.Result.Valid)
	require.NotEmpty(t, res.Fit.Rig)
	for i, m := range res.Fit.Rig {
		got := c.types[m.TypeID]
		require.Equal(t, want, got.attrs[attrRigSize], "rig %d", i)
		require.Equal(t, shapeKey(src), shapeKey(got), "rig %d keeps its family (group and effects)", i)
	}
	require.Greater(t, res.Result.DPS, res.Baseline.Flown.DPS, "the turrets keep ammo of the new charge size")
	// Back on a size-1 hull the small rigs fit as they are.
	res = convertSolve(t, c, f, hullTristan, nil)
	for i, m := range res.Fit.Rig {
		require.Equal(t, f.Rig[i].TypeID, m.TypeID)
	}
}

// A T3 source keeps its subsystems only on its own hull; elsewhere they are dropped. A
// Convert onto another T3 hull has no subsystems to use: no_candidate.
func TestConvert_T3SubsystemsDropped(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 4) // Tengu
	require.Len(t, f.Subsystem, 4)

	res := convertSolve(t, c, f, hullDrake, nil)
	require.Contains(t, []Status{StatusOK, StatusOKWithDowngrades}, res.Status)
	require.Empty(t, res.Fit.Subsystem)
	require.True(t, res.Result.Valid)
	n := 0
	for _, ch := range res.Changes {
		if ch.Group == SlotSubsystem {
			n++
			require.Equal(t, 0, ch.To)
		}
	}
	require.Equal(t, 4, n)

	res = convertSolve(t, c, f, hullTengu, nil)
	require.Equal(t, f.Subsystem, res.Fit.Subsystem)
	require.Empty(t, res.Changes)

	for _, src := range []fit.Fit{f, refFit(t, c, 3)} {
		target := hullLoki
		if src.HullID != hullTengu {
			target = hullTengu
		}
		res = convertSolve(t, c, src, target, nil)
		require.Equal(t, StatusNoCandidate, res.Status)
		require.True(t, res.NoCandidate.NeedsSubsystems)
	}
}

// A target hull the pilot cannot fly: no_candidate with the hull's missing skills.
func TestConvert_UnflyableTarget(t *testing.T) {
	c := realCatalog(t)
	p := e3aProfiles(t)["fitIII"] // flies the Rifter, not the Cerberus (Heavy Assault Cruisers 0)
	f := refFit(t, c, 1)
	res := convertSolve(t, c, f, hullCerberus, p)
	require.Equal(t, StatusNoCandidate, res.Status)
	require.NotNil(t, res.NoCandidate.Hull)
	require.Equal(t, hullCerberus, res.NoCandidate.Hull.TypeID)
	require.NotEmpty(t, res.NoCandidate.Hull.Missing)
	hac := c.Store().ResolveNames([]string{"Heavy Assault Cruisers"})["Heavy Assault Cruisers"]
	found := false
	for _, m := range res.NoCandidate.Hull.Missing {
		found = found || m.TypeID == hac
	}
	require.True(t, found, "Heavy Assault Cruisers is among the missing skills")
	require.Greater(t, res.Baseline.Flown.DPS, 0.0, "B2 is the Rifter fit on its own hull")
	require.LessOrEqual(t, res.Evaluations, 2)
}

// Drone quantities are capped by the target's bay (each line within its share) and by
// bandwidth; the result keeps the drones the bay holds.
func TestConvert_DroneQuantityCap(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 2) // Vexor: 15 drones, 125 m3
	res := convertSolve(t, c, f, hullThorax, nil)
	require.Contains(t, []Status{StatusOK, StatusOKWithDowngrades}, res.Status)
	require.True(t, res.Result.Valid)
	bay := 0.0
	for _, m := range res.Fit.Drones {
		bay += c.types[m.TypeID].attrs[attrVolume] * float64(m.Qty)
	}
	require.LessOrEqual(t, bay, c.types[hullThorax].attrs[attrShipBay])
	require.NotEmpty(t, res.Fit.Drones)

	// The line candidates themselves: no quantity above the line's bay share or the
	// bandwidth, and a line with no share left offers only the empty slot.
	s := &search{ctx: context.Background(), cat: c, req: Request{Kind: KindConvert, Fit: f, TargetHull: hullThorax},
		kind: KindConvert, pol: Fallback(KindConvert), hull: hullThorax, canUse: map[int]bool{}, down: map[[2]int]bool{}}
	nc, err := s.convertLines()
	require.NoError(t, err)
	require.Nil(t, nc)
	var drones []line
	for _, ln := range s.lines {
		if ln.ref.Slot == SlotDrone {
			drones = append(drones, ln)
		}
	}
	require.Len(t, drones, 3)
	bw := c.types[hullThorax].attrs[attrShipBW]
	for _, ln := range drones {
		for _, cd := range ln.cands {
			if cd.Empty() {
				continue
			}
			ct := c.types[cd.TypeID]
			require.LessOrEqual(t, float64(cd.Qty)*ct.attrs[attrVolume], c.types[hullThorax].attrs[attrShipBay])
			require.LessOrEqual(t, float64(cd.Qty)*ct.attrs[attrDroneBandwidth], bw)
		}
	}
	// 5 Hammerhead II take the 50 m3: the Hobgoblin and Valkyrie lines have no share.
	require.Equal(t, []Candidate{{}}, drones[1].cands)
	require.Equal(t, []Candidate{{}}, drones[2].cands)
}
