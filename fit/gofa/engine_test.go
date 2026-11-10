package gofa

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// TestEngine_ResolveAppliesModuleBonus verifies end-to-end engine orchestration:
// New(fakeSDE).Stats resolves a hull attribute that is boosted by a fitted module
// via a PostPercent ItemModifier (domain=shipID), with skill scaling applied.
//
// Setup:
//   - Hull (typeID 100): attr 40 = 1000.0 (base value to be boosted).
//     No effects on the hull itself.
//   - Module (typeID 200): attr 50 = 10.0 (modifyingAttr = +10% per level scaled
//     by the skill). Effect 99: ItemModifier, domain=shipID, modifiedAttr=40,
//     modifyingAttr=50, opPostPercent.
//   - No required-skill attrs on the module (DefaultSkills returns empty map).
//
// Expected post-resolution hull attr 40:
//
//	1000 * (1 + 10/100) = 1100.0
//	(no stacking penalty — single module; no skill multiplier in this test).
//
// Additionally, Stats must return FitStats{Estimated: false} (all stat groups are now populated).
func TestEngine_ResolveAppliesModuleBonus(t *testing.T) {
	const (
		hullTypeID   = 100
		moduleTypeID = 200
		effectID     = 99
		attrTarget   = 40 // attr on hull that gets boosted
		attrBonus    = 50 // attr on module that holds the bonus magnitude
	)

	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			hullTypeID:   {attrTarget: 1000.0},
			moduleTypeID: {attrBonus: 10.0},
		},
		effects: map[int][]int{
			moduleTypeID: {effectID},
		},
		modifiers: map[int][]sde.Modifier{
			effectID: {
				{
					Domain:        "shipID",
					Func:          "ItemModifier",
					ModifiedAttr:  attrTarget,
					ModifyingAttr: attrBonus,
					Operation:     opPostPercent,
				},
			},
		},
		attrMeta: map[int]sde.AttrMeta{
			attrTarget: {Stackable: true}, // exempt from stacking penalty
		},
	}

	f := fit.Fit{
		HullID:   hullTypeID,
		HullName: "TestHull",
		Mid:      []fit.FitModule{{TypeID: moduleTypeID, Name: "TestModule", Qty: 1}},
	}

	eng := New(s)
	ship, _, err := eng.resolve(f, fit.StatsOpts{})
	require.NoError(t, err)
	require.InDelta(t, 1100.0, ship.Attrs[attrTarget], 1e-6,
		"hull attr %d should be 1000*(1+10%%)", attrTarget)

	// Stats must return Estimated==false (all stat groups are now populated).
	stats, err := eng.Stats(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	require.False(t, stats.Estimated)
}

// TestEngine_ResolveMissingHull verifies that resolve returns an error when the
// hull typeID has no dogma data (GetDogma returns an empty map).
func TestEngine_ResolveMissingHull(t *testing.T) {
	s := &fakeSDE{
		dogma: map[int]map[int]float64{}, // hull typeID 999 not present
	}
	f := fit.Fit{HullID: 999, HullName: "Ghost"}
	eng := New(s)
	_, _, err := eng.resolve(f, fit.StatsOpts{})
	require.Error(t, err)
}

// TestEngine_ExpandQty verifies that a FitModule with Qty=3 expands to 3
// separate Items so the interpreter sees 3 stacking instances.
func TestEngine_ExpandQty(t *testing.T) {
	const (
		hullTypeID   = 1
		moduleTypeID = 2
		effectID     = 10
		attrTarget   = 40
		attrBonus    = 50
	)

	s := &fakeSDE{
		dogma: map[int]map[int]float64{
			hullTypeID:   {attrTarget: 1000.0},
			moduleTypeID: {attrBonus: 10.0},
		},
		effects: map[int][]int{
			moduleTypeID: {effectID},
		},
		modifiers: map[int][]sde.Modifier{
			effectID: {
				{
					Domain:        "shipID",
					Func:          "ItemModifier",
					ModifiedAttr:  attrTarget,
					ModifyingAttr: attrBonus,
					Operation:     opPostPercent,
				},
			},
		},
		attrMeta: map[int]sde.AttrMeta{
			attrTarget: {Stackable: false}, // penalized — stacking kicks in for 3 copies
		},
	}

	f := fit.Fit{
		HullID: hullTypeID,
		Mid:    []fit.FitModule{{TypeID: moduleTypeID, Qty: 3}},
	}

	eng := New(s)
	ship, items, err := eng.resolve(f, fit.StatsOpts{})
	require.NoError(t, err)

	// Count module items (all items minus hull).
	moduleCount := 0
	for _, it := range items {
		if it.TypeID == moduleTypeID {
			moduleCount++
		}
	}
	require.Equal(t, 3, moduleCount, "Qty=3 must expand to 3 Item instances")

	// Three penalized +10% bonuses — verify stacking is applied (result < 1300).
	want := 1000.0 *
		(1 + 0.10*stackingFactor(0)) *
		(1 + 0.10*stackingFactor(1)) *
		(1 + 0.10*stackingFactor(2))
	require.InDelta(t, want, ship.Attrs[attrTarget], 1e-6)
}

// One hand-built line with a huge Qty must fail fast with an error and allocate
// nothing per unit (SV-C1).
func TestEngine_ResolveRejectsHugeQty(t *testing.T) {
	s := &fakeSDE{dogma: map[int]map[int]float64{1: {40: 1000}, 2: {50: 10}}}
	huge := 2_000_000_000
	cases := []struct {
		name string
		f    fit.Fit
	}{
		{"drones", fit.Fit{HullID: 1, Drones: []fit.FitModule{{TypeID: 2, Qty: huge}}}},
		{"modules", fit.Fit{HullID: 1, Mid: []fit.FitModule{{TypeID: 2, Qty: huge}}}},
		{"split", fit.Fit{HullID: 1, Drones: []fit.FitModule{
			{TypeID: 2, Qty: maxDroneUnits}, {TypeID: 2, Qty: maxDroneUnits}}}},
	}
	for _, c := range cases {
		start := time.Now()
		_, _, err := New(s).resolve(c.f, fit.StatsOpts{})
		require.Error(t, err, c.name)
		require.Less(t, time.Since(start), wallBound(2*time.Second), c.name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, _, err := New(s).resolveForStats(ctx, fit.Fit{HullID: 1, Mid: []fit.FitModule{{TypeID: 2, Qty: 5}}}, fit.StatsOpts{})
	require.ErrorIs(t, err, context.Canceled)
}

// The worst shape the old 5000 budget admitted (2500 amplifiers x 2500 drones, about 6 s and 1.5 GiB
// of interpreter work) is refused before anything is built (SVR-I1).
func TestEngine_DominixQuantityShapeIsRefusedFast(t *testing.T) {
	s := openRealSDE(t)
	f, _ := fit.ParseEFT("[Dominix, x]\n\nDrone Damage Amplifier II x2500\n\n\n\n\nHobgoblin II x2500", s)
	require.NotEmpty(t, f.Drones)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := New(s).Stats(context.Background(), f, fit.StatsOpts{})
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	require.ErrorIs(t, err, fit.ErrFitTooLarge)
	if !raceEnabled { // a wall-clock bound is flaky under -race on a shared runner
		require.Less(t, took, time.Second)
	}
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(64<<20))
}

// A legal heavy shape (8 amplifiers, 50 drones) still computes the same numbers as on main.
func TestEngine_FullDroneBoatKeepsItsNumbers(t *testing.T) {
	s := openRealSDE(t)
	f, un := fit.ParseEFT("[Dominix, x]\n\nDrone Damage Amplifier II x8\n\n\n\n\nHobgoblin II x50", s)
	require.Empty(t, un)
	start := time.Now()
	st, err := New(s).Stats(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	if !raceEnabled { // a wall-clock bound is flaky under -race on a shared runner
		require.Less(t, time.Since(start), 2*time.Second)
	}
	require.InDelta(t, 256.513723702, st.DPS.Theoretical, 1e-6)
	require.InDelta(t, 256.513723702, st.Drones.DPS, 1e-6)
	require.Zero(t, st.Volley, "weapon volley: none (M6, fit parity)")
	require.InDelta(t, 1026.054894809, st.Drones.Volley, 1e-6) // was FitStats.Volley before M6
}

// Per-group caps: more than the slot range of a group is refused, at the cap it is accepted.
func TestEngine_GroupCaps(t *testing.T) {
	s := &fakeSDE{dogma: map[int]map[int]float64{1: {40: 1}, 2: {50: 1}}}
	ok := func(f fit.Fit) error { _, _, err := New(s).resolve(f, fit.StatsOpts{}); return err }
	require.NoError(t, ok(fit.Fit{HullID: 1, Low: []fit.FitModule{{TypeID: 2, Qty: 16}}}))
	require.ErrorIs(t, ok(fit.Fit{HullID: 1, Low: []fit.FitModule{{TypeID: 2, Qty: 9}, {TypeID: 2, Qty: 8}}}), fit.ErrFitTooLarge)
	require.NoError(t, ok(fit.Fit{HullID: 1, Rig: []fit.FitModule{{TypeID: 2, Qty: 6}}}))
	require.ErrorIs(t, ok(fit.Fit{HullID: 1, Rig: []fit.FitModule{{TypeID: 2, Qty: 7}}}), fit.ErrFitTooLarge)
	require.NoError(t, ok(fit.Fit{HullID: 1, Drones: []fit.FitModule{{TypeID: 2, Qty: maxDroneUnits}}}))
	require.ErrorIs(t, ok(fit.Fit{HullID: 1, Drones: []fit.FitModule{{TypeID: 2, Qty: maxDroneUnits + 1}}}), fit.ErrFitTooLarge)
}

// A pasted refit export lists spare slot modules in the cargo paragraph; ParseEFT places them by
// SDE slot, so a group can hold more than 8 units. Main computed this fit and so must we (SVR2-I1).
func TestEngine_RefitExportWithSpareModulesStillComputes(t *testing.T) {
	s := openRealSDE(t)
	eft := "[Abaddon, refit]\n" + strings.Repeat("Heat Sink II\n", 3) + strings.Repeat("Multispectrum Energized Membrane II\n", 4) +
		"\n" + strings.Repeat("Large Shield Extender II\n", 4) + "\n" +
		strings.Repeat("Mega Pulse Laser II, Multifrequency L\n", 8) + "\n\n\n" +
		"Multispectrum Energized Membrane II x2\nMobile Depot"
	f, _ := fit.ParseEFT(eft, s)
	require.Greater(t, len(f.Low), 0)
	st, err := New(s).Stats(context.Background(), f, fit.StatsOpts{})
	require.NoError(t, err)
	require.InDelta(t, 876.208021, st.DPS.Theoretical, 1e-5) // same as main
	require.InDelta(t, 113451.242425, st.Tank.TotalEHP, 1e-3)
}

// A module with a loaded charge costs two units against the total budget (SVR-M4).
func TestEngine_ChargeCountsTowardTheBudget(t *testing.T) {
	old := maxExpandedItems
	maxExpandedItems = 20
	t.Cleanup(func() { maxExpandedItems = old })
	s := &fakeSDE{dogma: map[int]map[int]float64{1: {40: 1}, 2: {50: 1}, 3: {51: 1}}}
	run := func(drones int) error {
		f := fit.Fit{HullID: 1,
			Drones: []fit.FitModule{{TypeID: 2, Qty: drones}},
			High:   []fit.FitModule{{TypeID: 2, ChargeID: 3, Qty: 4}}}
		_, _, err := New(s).resolve(f, fit.StatsOpts{})
		return err
	}
	// 4 charged modules = 8 units; 12 drones fill the budget of 20, 13 do not.
	require.NoError(t, run(12))
	require.ErrorIs(t, run(13), fit.ErrFitTooLarge)
}
