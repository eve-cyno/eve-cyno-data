package gofa

import (
	"context"
	"testing"

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
