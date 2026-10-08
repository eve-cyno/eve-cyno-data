package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde"

	"eve-cyno.dev/go/data/fit"
)

// noFlags is a zero flagsOf closure for tests that don't exercise the flag path.
func noFlags(int) (bool, bool) { return false, false }

// TestAssembleFitDetail exercises the pure assembler with fake inputs — no SDE required.
func TestAssembleFitDetail(t *testing.T) {
	// Ship id=1: calib=400, hi=3, med=4, low=3, rig=3
	shipDogma := map[int]float64{
		ATTR_CALIB_CAP: 400,
		ATTR_HI_SLOTS:  3,
		ATTR_MED_SLOTS: 4,
		ATTR_LOW_SLOTS: 3,
		ATTR_RIG_SLOTS: 3,
	}
	// Module id=2: high slot
	mod2Dogma := map[int]float64{
		ATTR_CALIB_COST: 0,
	}
	// Module id=3: mid slot
	mod3Dogma := map[int]float64{
		ATTR_CALIB_COST: 0,
	}

	dogma := map[int]map[int]float64{
		1: shipDogma,
		2: mod2Dogma,
		3: mod3Dogma,
	}

	nameToID := map[string]int{
		"Heavy Missile Launcher II": 2,
		"Large Shield Booster II":   3,
	}

	modules := []fit.EFTLine{
		{Section: "high", Name: "Heavy Missile Launcher II"},
		{Section: "mid", Name: "Large Shield Booster II"},
	}

	slotOf := func(id int) string {
		switch id {
		case 2:
			return "high"
		case 3:
			return "mid"
		}
		return ""
	}

	tierOf := func(id int) string {
		switch id {
		case 2:
			return "T2"
		case 3:
			return "T2"
		}
		return ""
	}

	fd := assembleFitDetail("Raven", 1, modules, nameToID, dogma, slotOf, tierOf, noFlags, "Battleship")

	// Ship metadata
	require.Equal(t, "Raven", fd.ShipName)
	require.Equal(t, "Battleship", fd.ShipClass)

	// Resources — calibration only: the CPU / PG totals come from the validator
	// (applyValidation), not from the assembler.
	require.InDelta(t, 400.0, fd.Resources.CalibCap, 0.001, "CalibCap from attr 1132")
	require.InDelta(t, 0.0, fd.Resources.CalibUsed, 0.001, "no rig calib usage")

	// High slot: launcher + 2 padded empties (cap=3)
	require.Len(t, fd.Slots.High, 3, "high: 1 module + 2 empty pads")
	require.Equal(t, "Heavy Missile Launcher II", fd.Slots.High[0].Name)
	require.Equal(t, "T2", fd.Slots.High[0].Tier)
	require.False(t, fd.Slots.High[0].Empty)
	require.True(t, fd.Slots.High[1].Empty)
	require.True(t, fd.Slots.High[2].Empty)

	// Mid slot: booster + 3 padded empties (cap=4)
	require.Len(t, fd.Slots.Mid, 4, "mid: 1 module + 3 empty pads")
	require.Equal(t, "Large Shield Booster II", fd.Slots.Mid[0].Name)
	require.Equal(t, "T2", fd.Slots.Mid[0].Tier)
	require.False(t, fd.Slots.Mid[0].Empty)
	require.True(t, fd.Slots.Mid[1].Empty)

	// Low and rig: all empty pads
	require.Len(t, fd.Slots.Low, 3, "low: 3 empty pads")
	for _, c := range fd.Slots.Low {
		require.True(t, c.Empty)
	}
	require.Len(t, fd.Slots.Rig, 3, "rig: 3 empty pads")

	// Always has the stats note
	require.NotEmpty(t, fd.Notes)
	hasStatsNote := false
	for _, n := range fd.Notes {
		if n == "DPS / EHP / cap-stable not modelled (deterministic data only)." {
			hasStatsNote = true
		}
	}
	require.True(t, hasStatsNote, "should include the stats disclaimer note")
}

// TestAssembleFitDetail_DroneSkip verifies that modules pre-filtered before assembleFitDetail
// (drones/charges) do not appear in slots. We test this by passing an empty modules list
// and confirming only empty pads appear.
func TestAssembleFitDetail_EmptyFit(t *testing.T) {
	shipDogma := map[int]float64{
		ATTR_CALIB_CAP: 0,
		ATTR_HI_SLOTS:  2,
		ATTR_MED_SLOTS: 2,
		ATTR_LOW_SLOTS: 2,
		ATTR_RIG_SLOTS: 0,
	}
	dogma := map[int]map[int]float64{1: shipDogma}
	fd := assembleFitDetail("Frigate Hull", 1, nil, nil, dogma,
		func(int) string { return "" },
		func(int) string { return "" },
		noFlags,
		"Frigate")

	require.Equal(t, "Frigate Hull", fd.ShipName)
	require.Len(t, fd.Slots.High, 2)
	require.Len(t, fd.Slots.Mid, 2)
	require.Len(t, fd.Slots.Low, 2)
	require.Len(t, fd.Slots.Rig, 0)
	require.InDelta(t, 0.0, fd.Resources.CalibUsed, 0.001)
}

// TestAssembleFitDetail_Flags verifies that canLoadCharge / canOverheat are set on FitCell
// when a real flagsOf closure is provided.
// typeID 1877 = Rapid Light Missile Launcher II (canLoadCharge=true, canOverheat=true)
// typeID 35659 = 50MN Y-T8 Compact Microwarpdrive (canLoadCharge=false, canOverheat=true)
func TestAssembleFitDetail_Flags(t *testing.T) {
	// Use static stub data — no SDE needed, we supply the flagsOf closure directly.
	shipDogma := map[int]float64{
		ATTR_HI_SLOTS:  1,
		ATTR_MED_SLOTS: 1,
	}
	launcherDogma := map[int]float64{}
	mwdDogma := map[int]float64{}
	dogma := map[int]map[int]float64{
		1:     shipDogma,
		1877:  launcherDogma,
		35659: mwdDogma,
	}
	nameToID := map[string]int{
		"Rapid Light Missile Launcher II":  1877,
		"50MN Y-T8 Compact Microwarpdrive": 35659,
	}
	modules := []fit.EFTLine{
		{Section: "high", Name: "Rapid Light Missile Launcher II"},
		{Section: "mid", Name: "50MN Y-T8 Compact Microwarpdrive"},
	}
	slotOf := func(id int) string {
		switch id {
		case 1877:
			return "high"
		case 35659:
			return "mid"
		}
		return ""
	}
	// Stub the real SDE flag values as constants (verified against sde.sqlite).
	flagsOf := func(id int) (bool, bool) {
		switch id {
		case 1877:
			return true, true // RLML II: canLoadCharge, canOverheat
		case 35659:
			return false, true // 50MN MWD: no charge, canOverheat
		}
		return false, false
	}

	fd := assembleFitDetail("Test Ship", 1, modules, nameToID, dogma,
		slotOf,
		func(int) string { return "T2" },
		flagsOf,
		"Cruiser")

	require.Len(t, fd.Slots.High, 1, "high slot has 1 module (cap=1)")
	launcher := fd.Slots.High[0]
	require.Equal(t, "Rapid Light Missile Launcher II", launcher.Name)
	require.True(t, launcher.CanLoadCharge, "launcher canLoadCharge must be true")
	require.True(t, launcher.CanOverheat, "launcher canOverheat must be true")

	require.Len(t, fd.Slots.Mid, 1, "mid slot has 1 module (cap=1)")
	mwd := fd.Slots.Mid[0]
	require.Equal(t, "50MN Y-T8 Compact Microwarpdrive", mwd.Name)
	require.False(t, mwd.CanLoadCharge, "MWD canLoadCharge must be false")
	require.True(t, mwd.CanOverheat, "MWD canOverheat must be true")
}

// TestBuildFitDetail_Flags is an SDE-gated integration test that verifies
// BuildFitDetail surfaces canLoadCharge and canOverheat on real FitCell data.
// Uses a minimal EFT with RLML II (launcher) and Damage Control II (passive low).
func TestBuildFitDetail_Flags(t *testing.T) {
	deps := liveDepsOrSkip(t)

	const eft = `[Caracal, Flag Test]
Rapid Light Missile Launcher II

Damage Control II
`
	fd, err := BuildFitDetail(context.Background(), deps, eft, false)
	require.NoError(t, err)
	require.NotEmpty(t, fd.ShipName)

	// Find the launcher in high slots.
	var launcher *FitCell
	for i := range fd.Slots.High {
		if fd.Slots.High[i].TypeID == 1877 {
			launcher = &fd.Slots.High[i]
			break
		}
	}
	require.NotNil(t, launcher, "RLML II (1877) should appear in high slots")
	require.True(t, launcher.CanLoadCharge, "RLML II canLoadCharge must be true")
	require.True(t, launcher.CanOverheat, "RLML II canOverheat must be true")

	// Find the damage control in low slots.
	var dcu *FitCell
	for i := range fd.Slots.Low {
		if fd.Slots.Low[i].TypeID == 2048 {
			dcu = &fd.Slots.Low[i]
			break
		}
	}
	require.NotNil(t, dcu, "Damage Control II (2048) should appear in low slots")
	require.False(t, dcu.CanLoadCharge, "DCU canLoadCharge must be false")
	require.False(t, dcu.CanOverheat, "DCU canOverheat must be false")
}

// TestAssembleFitDetail_UnresolvedModule verifies that a module with no nameToID entry
// is skipped and a Note is added.
func TestAssembleFitDetail_UnresolvedModule(t *testing.T) {
	shipDogma := map[int]float64{
		ATTR_HI_SLOTS: 1,
	}
	dogma := map[int]map[int]float64{1: shipDogma}
	modules := []fit.EFTLine{
		{Section: "high", Name: "Nonexistent Module XYZ"},
	}
	fd := assembleFitDetail("Ship", 1, modules, map[string]int{}, dogma,
		func(int) string { return "" },
		func(int) string { return "" },
		noFlags,
		"")

	// The module is not placed (no typeID) — only empty pad
	require.Len(t, fd.Slots.High, 1)
	require.True(t, fd.Slots.High[0].Empty)

	// Reported in the first-class field, not as a note.
	require.Equal(t, []string{"Nonexistent Module XYZ"}, fd.Unresolved)
	require.False(t, fd.Valid)
}

// Item 8 (#130): the footer CPU / PG are the validator's, not x1.25 of the raw dogma.
// Rifter + 200mm AutoCannon II + a Reactor Control Unit II, all-V skills:
//   - CPU cap = hull CPU x 1.25; PG cap = hull PG x 1.25 x RCU II bonus
//   - 200mm AutoCannon II draws its raw CPU / PG with Weapon Upgrades V (-25 % CPU), the RCU its CPU with Energy Grid Upgrades V (-25 %)
//     and Advanced Weapon Upgrades V (-10 % PG).
func TestBuildFitDetail_ResourcesComeFromTheValidator(t *testing.T) {
	s, err := sde.Open(realSDEPathOrSkip(t))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ids := s.ResolveNames([]string{"Rifter", "200mm AutoCannon II", "Reactor Control Unit II"})
	hull, gun, rcu := s.GetDogma(ids["Rifter"]), s.GetDogma(ids["200mm AutoCannon II"]), s.GetDogma(ids["Reactor Control Unit II"])

	fd, err := BuildFitDetail(context.Background(), &Deps{SDE: s}, "[Rifter, r]\n200mm AutoCannon II\n\n\nReactor Control Unit II\n", false)
	require.NoError(t, err)

	require.InDelta(t, hull[48]*1.25, fd.Resources.CPUCap, 1e-6, "CPU cap: hull x all-V skill")
	require.InDelta(t, (gun[50]+rcu[50])*0.75, fd.Resources.CPUUsed, 1e-6, "turret and power-core CPU with Weapon Upgrades V / Energy Grid Upgrades V (both -25 %)")
	require.InDelta(t, gun[30]*0.90+rcu[30], fd.Resources.PGUsed, 1e-6, "turret PG with Advanced Weapon Upgrades V")
	require.Greater(t, fd.Resources.PGCap, hull[11]*1.25, "the RCU lifts the powergrid cap")
	// The legacy math (raw load, no skill discounts) differs.
	require.Less(t, fd.Resources.CPUUsed, gun[50]+rcu[50])
}
