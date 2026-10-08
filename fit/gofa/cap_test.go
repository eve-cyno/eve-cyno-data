package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCapacitor_NoModules verifies that a ship with zero module cap usage is
// stable and capacity matches the ship attribute.
func TestCapacitor_NoModules(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,   // GJ
			attrCapRechargeMs: 200_000, // 200 s → recharge 200 s
		},
	}
	// No modules: totalUsage = 0, peakRecharge = 2.5*500/200 = 6.25 GJ/s → stable.
	got := capacitor(ship, []*Item{ship})
	require.True(t, got.Stable, "no active modules → must be stable")
	require.InDelta(t, 500.0, got.Capacity, 1e-9)
	require.InDelta(t, 200.0, got.RechargeSec, 1e-9)
	require.InDelta(t, 100.0, got.StablePct, 1e-9, "zero drain → 100%% stable")
	require.InDelta(t, 0.0, got.SecondsToEmpty, 1e-9)
}

// TestCapacitor_CapDraining verifies that a module consuming more GJ/s than the
// peak recharge rate results in an unstable capacitor.
func TestCapacitor_CapDraining(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   100.0,
			attrCapRechargeMs: 100_000, // 100 s → recharge=2.5*100/100=2.5 GJ/s peak
		},
	}
	// Module: 5 GJ per 1000 ms = 5 GJ/s >> 2.5 GJ/s peak → unstable.
	module := &Item{
		TypeID: 200,
		Kind:   KindModule,
		Attrs: map[int]float64{
			attrCapNeed:        5.0,
			attrModuleDuration: 1000.0, // ms
		},
	}
	got := capacitor(ship, []*Item{ship, module})
	require.False(t, got.Stable, "module drain 5 GJ/s > peak 2.5 GJ/s → unstable")
	require.InDelta(t, 100.0, got.Capacity, 1e-9)
	// SecondsToEmpty ≈ 100 / (5 - 2.5) = 40 s.
	require.InDelta(t, 40.0, got.SecondsToEmpty, 1e-6)
}

// TestCapacitor_ZeroRecharge verifies that a ship with zero recharge time
// returns a zero-valued CapStats without panicking.
func TestCapacitor_ZeroRecharge(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   100.0,
			attrCapRechargeMs: 0, // pathological input
		},
	}
	got := capacitor(ship, nil)
	require.InDelta(t, 100.0, got.Capacity, 1e-9)
	// Should not panic; stable/secsToEmpty/stablePct are zero.
}

// TestCapacitor_CapBoosterInjection verifies that a cap-booster charge's
// capacitorBonus (attr 67) is counted as income, making a fit stable that would
// otherwise be unstable on passive recharge alone.
//
// Setup mirrors the maller-cap-heavy oracle:
//   - peakRecharge = 2.5 × 2031.25 / 279 ≈ 18.19 GJ/s
//   - Large Armor Repairer II: 400 GJ / 11.25 s ≈ 35.56 GJ/s  (drain)
//   - EM Armor Hardener II:     30 GJ / 20 s    =  1.5  GJ/s  (drain)
//   - Thermal Armor Hardener II:30 GJ / 20 s    =  1.5  GJ/s  (drain)
//   - Medium Cap Booster II + Cap Booster 800: 800 GJ / 12 s ≈ 66.67 GJ/s (injection)
//
// Without injection: totalDrain ≈ 38.56 > peakRecharge 18.19 → unstable.
// With injection:    peakRecharge + 66.67 ≈ 84.86 > 38.56 → stable.
func TestCapacitor_CapBoosterInjection(t *testing.T) {
	ship := &Item{
		TypeID: 624,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   2031.25,
			attrCapRechargeMs: 279_000.0, // Maller resolved recharge ≈ 279 s
		},
	}
	// Large Armor Repairer II: 400 GJ / 11250 ms
	lar := &Item{TypeID: 3540, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 400.0, attrModuleDuration: 11_250.0,
	}}
	// EM Armor Hardener II: 30 GJ / 20000 ms
	emHard := &Item{TypeID: 11642, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 30.0, attrModuleDuration: 20_000.0,
	}}
	// Thermal Armor Hardener II: 30 GJ / 20000 ms
	thermHard := &Item{TypeID: 11648, Kind: KindModule, Attrs: map[int]float64{
		attrCapNeed: 30.0, attrModuleDuration: 20_000.0,
	}}
	// Medium Capacitor Booster II: duration 12000 ms, no cap need of its own
	// (the cap injection comes from the loaded charge; gofa reads the charge attr).
	capBooster := &Item{TypeID: 2024, Kind: KindModule, Attrs: map[int]float64{
		attrModuleDuration: 12_000.0,
		// capacitorNeed deliberately absent — booster cost covered by charge injection
	}}
	// Cap Booster 800 charge: capacitorBonus = 800 GJ (attr 67).
	charge800 := &Item{TypeID: 11289, Kind: KindCharge, Attrs: map[int]float64{
		attrCapacitorBonus: 800.0,
	}}

	items := []*Item{ship, lar, emHard, thermHard, capBooster, charge800}

	got := capacitor(ship, items)
	require.True(t, got.Stable,
		"cap booster injection (66.7 GJ/s) + passive recharge must exceed drain (38.6 GJ/s)")
	require.InDelta(t, 2031.25, got.Capacity, 1e-6)
}

// TestCapacitor_TurretCycleFallback verifies that turrets and launchers — which
// carry their cycle time in attr 51 (speed) rather than attr 73 (duration) — are
// counted as cap usage.  The fix in cap.go falls back to attrCapCycleSpeed (51)
// when attrModuleDuration (73) is zero or absent.
//
// Setup: a ship whose peak recharge is 6.25 GJ/s, loaded with blasters that
// have only attr 51 (cycle 4611 ms) and attr 6 (cap need 8 GJ).
//
//	blaster rate = 8 / (4611/1000) ≈ 1.734 GJ/s
//
// Eight blasters: 8 × 1.734 ≈ 13.87 GJ/s > peak 6.25 GJ/s → unstable.
// Without the attr 51 fallback all blasters would be skipped (dur=0) and the
// cap would appear stable — the opposite of the correct answer.
func TestCapacitor_TurretCycleFallback(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,
			attrCapRechargeMs: 200_000, // peakRecharge = 2.5*500/200 = 6.25 GJ/s
		},
	}
	// Simulate a Neutron Blaster Cannon II: no attr 73, only attr 51 (cycle 4611 ms)
	// and attr 6 (cap need 8 GJ/cycle).
	blaster := func() *Item {
		return &Item{
			TypeID: 3057,
			Kind:   KindModule,
			Attrs: map[int]float64{
				attrCapNeed:       8.0,
				attrCapCycleSpeed: 4611.0, // attr 51 — no attr 73 present
			},
		}
	}
	items := []*Item{ship}
	// Eight blasters: 8 × (8/4.611) ≈ 13.87 GJ/s > peakRecharge 6.25 GJ/s → unstable.
	for i := 0; i < 8; i++ {
		items = append(items, blaster())
	}

	got := capacitor(ship, items)
	require.False(t, got.Stable,
		"turret cap drain via attr 51 fallback: 8 blasters at ≈13.87 GJ/s must exceed peak 6.25 GJ/s")
}

// TestCapacitor_ModuleWithZeroDuration verifies that a module with duration=0
// is skipped (not counted as cap usage), leaving the cap stable.
func TestCapacitor_ModuleWithZeroDuration(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrCapCapacity:   500.0,
			attrCapRechargeMs: 100_000,
		},
	}
	// Module with zero duration should be ignored.
	module := &Item{
		TypeID: 200,
		Kind:   KindModule,
		Attrs: map[int]float64{
			attrCapNeed:        10.0,
			attrModuleDuration: 0, // zero → skipped
		},
	}
	got := capacitor(ship, []*Item{ship, module})
	require.True(t, got.Stable, "zero-duration module must be skipped")
}
