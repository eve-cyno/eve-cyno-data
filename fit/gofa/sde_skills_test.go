package gofa

// sde_skills_test.go — empirical acceptance tests against the real SDE.
// These require data/sde/sde.sqlite and are skipped when it is absent.
//
// Reference numbers were cross-checked against Pyfa v2.67.0 output (a local
// oracle run outside this repository) and confirmed on
// 2026-06-15 against the SDE dump. Only the numbers are used.

import (
	"testing"

	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// openRealSDE opens the project SDE, skipping the test when the file is absent.
func openRealSDE(t *testing.T) *realsde.SDE {
	t.Helper()
	p := sdetest.Path(t)
	s, err := realsde.Open(p)
	if err != nil || !s.Available() {
		sdetest.Skip(t, "data/sde/sde.sqlite not available; skipping real-SDE test")
	}
	return s
}

// TestRifter_150mmAutoCannon_RoF_AllV is the decisive empirical acceptance test.
//
// It resolves a Rifter (587) + one 150mm Light AutoCannon I (485) at all-V
// through the full engine and asserts the turret's resolved attr 51 (RoF/speed,
// ms) matches the reference value (cross-checked against Pyfa output) within ±0.5%.
//
// The result is produced entirely by data-driven dogma effects (no hardcoded
// level scaling): the Minmatar Frigate, Gunnery and Rapid Firing skills carry
// PreMul-by-skillLevel effects that scale their per-level bonuses, which the
// hull/skill effects then apply:
//
//	3375 × MinmatarFrigate-V (0.625) × Gunnery-V (0.90) × RapidFiring-V (0.80) = 1518.75 ms
//
// Reference value cross-checked against Pyfa output: 1518.75 ms (2026-06-15).
func TestRifter_150mmAutoCannon_RoF_AllV(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()

	const (
		rifterTypeID  = 587
		autocannon485 = 485 // 150mm Light AutoCannon I
		attrSpeed     = 51  // RoF / speed (ms per shot)
		oracleAttr51  = 1518.75
	)

	f := fit.Fit{
		HullID:   rifterTypeID,
		HullName: "Rifter",
		High:     []fit.FitModule{{TypeID: autocannon485, Name: "150mm Light AutoCannon I", Qty: 1}},
	}

	_, items, err := New(s).resolve(f, fit.StatsOpts{})
	require.NoError(t, err)

	var ac *Item
	for _, it := range items {
		if it.TypeID == autocannon485 {
			ac = it
			break
		}
	}
	require.NotNil(t, ac, "autocannon must be present in the resolved items")
	require.InDelta(t, oracleAttr51, ac.Attrs[attrSpeed], oracleAttr51*0.005,
		"gofa attr51 (RoF ms) must match the oracle value within ±0.5%% (oracle=%.4f gofa=%.4f)", oracleAttr51, ac.Attrs[attrSpeed])
}
