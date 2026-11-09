package gofa

import (
	"context"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// TestAmmoRangeModifiers: the loaded charge reaches its turret through the otherID
// effects 596 (weaponRangeMultiplier 120, PreMul on maxRange 54) and 599 (fallofMultiplier
// 517, PostMul on falloff 158). Hand values from the SDE: the figure before the charge
// was applied times the charge multiplier.
//
//	rifter-turret    falloff 5160 x 1.5 (hull, level V) x 1.25 (Trajectory Analysis V) = 9675; Barrage S x1.5 = 14512.5
//	firetail-mwd     falloff 8062.5 x 1.5 = 12093.75
//	harbinger-laser  optimal 19687.5 x 0.5 (Multifrequency M) = 9843.75
//	thorax-blaster   optimal 9000 x 0.5 (Antimatter Charge M) = 4500
//	qc2 Ikitursa     optimal 18000 x 1.71875 x 0.6 (Occult M) = 18562.5
//
// EVE University "Turrets" (https://wiki.eveuniversity.org/Turrets): Multifrequency and
// Antimatter -50 % optimal.
func TestAmmoRangeModifiers(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	want := map[string][2]float64{ // optimal, falloff
		"rifter-turret":         {1500, 14512.5},
		"firetail-mwd":          {1500, 12093.75},
		"harbinger-laser":       {9843.75, 7812.5},
		"thorax-blaster":        {4500, 12500},
		"qc2/q202_ikitursa.eft": {18562.5, 0},
	}
	for _, pf := range loadPerfFits(t, s) {
		w, ok := want[pf.name]
		if !ok {
			continue
		}
		st, err := eng.Stats(context.Background(), pf.fit, fit.StatsOpts{})
		require.NoError(t, err)
		require.Len(t, st.Range, 1, pf.name)
		require.InDelta(t, w[0], st.Range[0].OptimalM, 1e-6, pf.name)
		require.InDelta(t, w[1], st.Range[0].FalloffM, 1e-6, pf.name)
		delete(want, pf.name)
	}
	require.Empty(t, want)
}
