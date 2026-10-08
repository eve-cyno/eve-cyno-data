package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTank_LayerEHP verifies the EHP formula for a single layer.
//
// EHP = HP / mean(resonances)
// With HP=1000, all four resonances=0.5: mean=0.5, EHP=2000.
func TestTank_LayerEHP(t *testing.T) {
	ehp := layerEHP(1000, 0.5, 0.5, 0.5, 0.5)
	require.InDelta(t, 2000.0, ehp, 1e-9)
}

// TestTank_LayerEHP_MixedResonances checks a non-uniform resonance set.
//
// HP=1200, EM=0.4, Therm=0.6, Kin=0.5, Exp=0.5 → mean=0.5 → EHP=2400.
func TestTank_LayerEHP_MixedResonances(t *testing.T) {
	ehp := layerEHP(1200, 0.4, 0.6, 0.5, 0.5)
	require.InDelta(t, 2400.0, ehp, 1e-9)
}

// TestTank_ZeroHP checks that a layer with zero HP contributes zero EHP
// regardless of resonances.
func TestTank_ZeroHP(t *testing.T) {
	ehp := layerEHP(0, 0.5, 0.5, 0.5, 0.5)
	require.Equal(t, 0.0, ehp)
}

// TestTank_NoResists checks that a layer with no resist attributes (all
// resonance=1.0) returns HP as EHP.
func TestTank_NoResists(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrShieldHP: 1000.0,
			attrArmorHP:  500.0,
			attrHullHP:   400.0,
			// No resonance attrs → defaults to 1.0 (no resist).
		},
	}
	stats := tank(ship, nil)
	// With resonance=1.0 for all layers: EHP=HP for each layer.
	require.InDelta(t, 1000.0, stats.ShieldHP, 1e-9)
	require.InDelta(t, 1000.0+500.0+400.0, stats.TotalEHP, 1e-9)
	// ResistProfile must be zero (1-1.0=0.0).
	require.InDelta(t, 0.0, stats.ShieldResists.EM, 1e-9)
}

// TestTank_KineticShieldResist verifies EHP for a shield layer with a single
// kinetic resonance of 0.5 (50% kinetic resist) and no other resists (1.0).
//
// HP=1000, resonances=(1.0, 1.0, 0.5, 1.0) → mean=0.875 → EHP≈1142.86.
func TestTank_KineticShieldResist(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrShieldHP:     1000.0,
			attrArmorHP:      0.0,
			attrHullHP:       0.0,
			attrShieldResKin: 0.5,
			// EM, Therm, Exp absent → default 1.0
		},
	}
	stats := tank(ship, nil)
	// mean = (1.0+1.0+0.5+1.0)/4 = 3.5/4 = 0.875
	want := 1000.0 / 0.875
	require.InDelta(t, want, stats.TotalEHP, 1e-6)
	// Shield kinetic resist = 1 − 0.5 = 0.5.
	require.InDelta(t, 0.5, stats.ShieldResists.Kin, 1e-9)
}

// TestTank_ActiveRep verifies that a shield booster module contributes to
// ActiveRepPerSec: boost/durSec.
func TestTank_ActiveRep(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrShieldHP: 500.0,
			attrArmorHP:  500.0,
			attrHullHP:   500.0,
		},
	}
	// Module: 100 HP boost per 5000 ms → 20 HP/s.
	booster := &Item{
		TypeID: 100,
		Kind:   KindModule,
		Attrs: map[int]float64{
			attrShieldBoostAmount: 100.0,
			attrModuleDuration:    5000.0, // ms
		},
	}
	stats := tank(ship, []*Item{ship, booster})
	require.InDelta(t, 20.0, stats.ActiveRepPerSec, 1e-9,
		"100 HP / 5 s = 20 HP/s")
}
