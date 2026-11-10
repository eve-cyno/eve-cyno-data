package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// M1 (fit parity): the Cerberus gives 5 % per Caldari Cruiser level to kinetic missile damage
// and 2.5 % to the other types. The SDE has one row (shipBonusCC 2.5) for all four.

// Fails when the SDE row changes: the correction in corrections.go is then wrong or no longer
// needed, and must be removed or re-derived.
func TestCorrections_CerberusSDERowUnchanged(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	require.InDelta(t, 2.5, s.GetDogma(11993)[487], 1e-9, "Cerberus shipBonusCC; if the SDE now carries a separate 5 % kinetic row, delete the correction")
	for _, id := range []int{899, 5383, 5384, 5385} {
		require.Contains(t, s.GetTypeEffectIDs(11993), id)
	}
	// Hulls that share the pattern need no entry: equal bonus for all four types in the SDE.
	require.InDelta(t, 15, s.GetDogma(33818)[487], 1e-9, "Orthrus")
	require.InDelta(t, 20, s.GetDogma(60764)[487], 1e-9, "Laelaps")
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
