package gofa

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNavigation_AlignTime checks the align-time formula with hand-built attrs.
//
// Formula: alignTime = -ln(0.25) * mass * agility / 1_000_000
//
// With mass=1_000_000 kg and agility=3.0:
//
//	alignTime = -ln(0.25) * 1e6 * 3.0 / 1e6 = -ln(0.25) * 3 ≈ 4.159 s
//
// Note: the EVE formula uses -ln(0.25) = ln(4) = 2*ln(2), not ln(2).
func TestNavigation_AlignTime(t *testing.T) {
	ship := &Item{
		TypeID: 1,
		Kind:   KindShip,
		Attrs: map[int]float64{
			attrNavMaxVelocity: 500.0,
			attrNavMass:        1_000_000,
			attrNavAgility:     3.0,
			attrNavSigRadius:   35.0,
		},
	}

	got := navigation(ship)

	wantAlign := -math.Log(0.25) * 3.0 // = ln(4)*mass*agility/1e6 = ln(4)*3 ≈ 4.159
	require.InDelta(t, wantAlign, got.AlignTimeSec, 1e-9,
		"alignTime = -ln(0.25)*mass*agility/1e6 (mass=1e6, agility=3.0)")
	require.InDelta(t, 500.0, got.MaxVelocity, 1e-9, "maxVelocity pass-through")
	require.InDelta(t, 35.0, got.SignatureRadius, 1e-9, "signatureRadius pass-through")
}

// TestNavigation_ZeroAttrs checks that navigation handles a ship with no attrs
// gracefully (returns zero values).
func TestNavigation_ZeroAttrs(t *testing.T) {
	ship := &Item{TypeID: 1, Kind: KindShip, Attrs: map[int]float64{}}
	got := navigation(ship)
	require.Equal(t, 0.0, got.MaxVelocity)
	require.Equal(t, 0.0, got.AlignTimeSec)
	require.Equal(t, 0.0, got.SignatureRadius)
}
