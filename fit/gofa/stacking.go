package gofa

import (
	"math"
	"sort"
)

// stackingFactor returns the diminishing multiplier applied to the i-th
// strongest stacking-penalized bonus (0-indexed). It uses the publicly
// documented curve exp(-(i/2.67)^2) (EVE University wiki, "Stacking penalties"):
// factor[0]=1.0, factor[1]≈0.8691, factor[2]≈0.5706, factor[3]≈0.2829, and so on.
func stackingFactor(i int) float64 {
	return math.Exp(-math.Pow(float64(i)/2.67, 2))
}

// applyStacked applies a set of fractional bonuses (e.g. 0.10 for +10%) to base
// under EVE's stacking penalty. Bonuses are taken strongest-magnitude first and
// each is scaled by stackingFactor(rank), so the largest bonus is unpenalized
// and successive ones contribute progressively less. The input slice is not
// mutated.
func applyStacked(base float64, bonuses []float64) float64 {
	sorted := make([]float64, len(bonuses))
	copy(sorted, bonuses)
	sort.SliceStable(sorted, func(a, b int) bool {
		return math.Abs(sorted[a]) > math.Abs(sorted[b])
	})

	result := base
	for i, bonus := range sorted {
		result *= 1 + bonus*stackingFactor(i)
	}
	return result
}
