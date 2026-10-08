package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackingFactor(t *testing.T) {
	require.InDelta(t, 1.0, stackingFactor(0), 1e-3)
	require.InDelta(t, 0.8691, stackingFactor(1), 1e-3)
	require.InDelta(t, 0.5706, stackingFactor(2), 1e-3)
	require.InDelta(t, 0.2829, stackingFactor(3), 1e-3)
}

func TestApplyStacked(t *testing.T) {
	// Unsorted input; the helper must apply the strongest bonus first.
	want := 100 * (1 + 0.10*stackingFactor(0)) * (1 + 0.05*stackingFactor(1))
	require.InDelta(t, want, applyStacked(100, []float64{0.05, 0.10}), 1e-6)

	// Explicit reference value cross-check. The literal 0.8691 is a rounded
	// stacking factor, so allow a tolerance that covers that rounding.
	require.InDelta(t, 100*(1+0.10*1.0)*(1+0.05*0.8691), applyStacked(100, []float64{0.05, 0.10}), 1e-3)

	// No bonuses returns base unchanged.
	require.InDelta(t, 100, applyStacked(100, nil), 1e-9)

	// Input slice must not be mutated.
	in := []float64{0.05, 0.10}
	applyStacked(100, in)
	require.Equal(t, []float64{0.05, 0.10}, in)
}
