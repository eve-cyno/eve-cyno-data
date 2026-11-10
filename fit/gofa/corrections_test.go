package gofa

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// M1 (fit parity): the Cerberus gives 5 % per Caldari Cruiser level to kinetic missile damage
// and 2.5 % to the other types. The SDE has one row (shipBonusCC 2.5) for all four.

// The Cerberus row differs between SDE builds. Old build (up to 3670638): effect 899 reads shipBonusCC
// (487) = 2.5 and the correction doubles it. Build 3682781 (2026-11-10) fixed it at the source:
// effect 899 is gone, effect 13056 shipMissileKineticDamageCC3 reads shipBonusCC3 (1535) = 5, and 487
// stays 2.5 for EM, thermal and explosive. Either way the game's 5 % kinetic and 2.5 % others hold.
func TestCorrections_CerberusRowMatchesOneKnownBuild(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	effects := s.GetTypeEffectIDs(11993)
	require.InDelta(t, 2.5, s.GetDogma(11993)[487], 1e-9, "Cerberus shipBonusCC feeds EM, thermal, explosive on both builds")
	for _, id := range []int{5383, 5384, 5385} {
		require.Contains(t, effects, id)
	}
	if slices.Contains(effects, 899) {
		// Old build: the correction applies, the data does not carry the 5 %.
		require.NotContains(t, effects, 13056)
		require.Equal(t, 2.0, magnitudeScale(11993, 899, 487))
	} else {
		// New build: the data carries the 5 %, the correction is inert.
		require.Contains(t, effects, 13056, "kinetic effect moved to 13056")
		require.InDelta(t, 5, s.GetDogma(11993)[1535], 1e-9, "shipBonusCC3")
	}
	// Hulls that share the pattern need no entry: equal bonus for all four types in the SDE.
	require.InDelta(t, 15, s.GetDogma(33818)[487], 1e-9, "Orthrus")
	require.InDelta(t, 20, s.GetDogma(60764)[487], 1e-9, "Laelaps")
}

// The correction is inert unless the SDE row is the old one: any other effect, type or
// modifying attribute is never scaled (so a fixed row is not scaled to 7.5 % or 10 %).
func TestCorrections_ScaleIsInertForAnythingElse(t *testing.T) {
	require.Equal(t, 2.0, magnitudeScale(11993, 899, 487))
	require.Equal(t, 1.0, magnitudeScale(11993, 899, 1535), "effect 899 reading the corrected attribute")
	require.Equal(t, 1.0, magnitudeScale(11993, 13056, 1535), "the new kinetic effect")
	require.Equal(t, 1.0, magnitudeScale(11993, 5383, 487), "EM keeps 2.5 %")
	require.Equal(t, 1.0, magnitudeScale(33818, 899, 487), "another hull")
}

// Game, owner check 2026-11-10: DPS 356.6, volley 1,837 (main: Caldari Cruiser 5, Heavy Missile
// Specialization 4, Warhead Upgrades 5, everything else V).
func TestCorrections_CerberusMainMatchesGame(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 3)
	st := statsOf(t, eng, f, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillHeavyMissileSpec: 4})})
	within(t, st.DPS.Theoretical, 356.6, 0.01, "DPS")
	require.InDelta(t, 1837, st.Volley, 1837*0.01)
}

// Pins that only the kinetic effect is doubled: with Caldari Cruiser 3 the hull factor is
// 1 + 0.05 x 3 = 1.15 on kinetic. Volley derived from the all-V figure 1,837.12 / 1.25 x 1.15.
func TestCorrections_CerberusKineticOnly(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 3)
	st := statsOf(t, eng, f, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillHeavyMissileSpec: 4, skillCaldariCruiser: 3})})
	require.InDelta(t, 1837.12/1.25*1.15, st.Volley, 1.0)

	// EM keeps 2.5 % per level: Mjolnir (EM) at Caldari Cruiser 3 is x1.075 on the hull. Ratio of
	// the EM volley at Caldari Cruiser 5 to 3 must be 1.125 / 1.075, not 1.25 / 1.15.
	em := parityFitWith(t, s, 3, "Scourge Heavy Missile", "Mjolnir Heavy Missile")
	v5 := statsOf(t, eng, em, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillHeavyMissileSpec: 4})}).Volley
	v3 := statsOf(t, eng, em, fit.StatsOpts{Skills: allVWith(eng, map[int]int{skillHeavyMissileSpec: 4, skillCaldariCruiser: 3})}).Volley
	require.InDelta(t, 1.125/1.075, v5/v3, 1e-4)
}
