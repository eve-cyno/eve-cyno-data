package fit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeSearcher struct{ mods []ModuleCandidate }

func (f fakeSearcher) Search(_ string, _ int) []ModuleCandidate { return f.mods }

type fakeSim struct{ fm []FreqMod }

func (f fakeSim) SimilarModules(_ int, _ string) []FreqMod { return f.fm }

func TestSuggest_FiltersToSlotAndHeadroom(t *testing.T) {
	searcher := fakeSearcher{mods: []ModuleCandidate{
		{TypeID: 1877, Name: "Rapid Light Missile Launcher II", Slot: "high", MetaTier: "T2", CPU: 18, PG: 0},
		{TypeID: 999, Name: "Huge High Module", Slot: "high", MetaTier: "T1", CPU: 9000, PG: 0}, // won't fit
		{TypeID: 2048, Name: "Damage Control II", Slot: "low", MetaTier: "T2", CPU: 20, PG: 1},  // wrong slot
	}}
	sim := fakeSim{fm: []FreqMod{{TypeID: 1877, Count: 8, Total: 10}}}

	out := Suggest(SuggestInput{Slot: "high", HullTypeID: 17715, CPUFree: 50, PGFree: 20}, searcher, sim, 12)

	require.NotEmpty(t, out)
	for _, s := range out {
		require.Equal(t, "high", s.Slot, "only high-slot candidates")
	}
	// The frequent, fittable launcher ranks first with a community reason.
	require.Equal(t, 1877, out[0].TypeID)
	require.True(t, out[0].Fits)
	require.Equal(t, "in 80% of similar fits", out[0].Reason)
	// The over-budget high module is present but marked not-fitting and ranked last.
	last := out[len(out)-1]
	require.Equal(t, 999, last.TypeID)
	require.False(t, last.Fits)
}

func TestSuggest_NoSimilarFallsBackToPool(t *testing.T) {
	searcher := fakeSearcher{mods: []ModuleCandidate{
		{TypeID: 1, Name: "B Module", Slot: "mid", MetaTier: "T1", CPU: 1},
		{TypeID: 2, Name: "A Module", Slot: "mid", MetaTier: "T2", CPU: 1},
	}}
	out := Suggest(SuggestInput{Slot: "mid", CPUFree: 100, PGFree: 100}, searcher, fakeSim{}, 12)
	require.Len(t, out, 2)
	// Higher meta tier wins the tie when no community frequency exists.
	require.Equal(t, 2, out[0].TypeID)
	require.Contains(t, out[0].Reason, "fittable")
}
