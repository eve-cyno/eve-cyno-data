package solve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// Owner ruling 2026-11-09 on the pinned build: the candidates of HML II hold Faction,
// Storyline and Officer launchers too, Officer last of the launchers, and the cap holds.
func TestCandidates_AnyMetaGroup_RealSDE(t *testing.T) {
	s, err := realsde.Open(sdetest.Path(t))
	if err != nil || !s.Available() {
		sdetest.Skip(t, "data/sde/sde.sqlite not available; skipping real-SDE test")
	}
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	c := NewCatalog(sub)
	r := c.Store().ResolveNames([]string{"Heavy Missile Launcher II", "Caldari Navy Heavy Missile Launcher", "Heavy Missile Launcher I"})
	require.Len(t, r, 3)
	t2, navy, t1 := r["Heavy Missile Launcher II"], r["Caldari Navy Heavy Missile Launcher"], r["Heavy Missile Launcher I"]

	o := Options{Kind: KindRefit, Skills: nil, Policy: Fallback(KindRefit)}
	f := fit.Fit{HullID: 24690, High: []fit.FitModule{{TypeID: t2, Qty: 1}}} // Drake
	got := c.Candidates(f, hi0, o)
	require.Equal(t, t2, got[0].TypeID)
	require.LessOrEqual(t, len(got), MaxCandidates)
	groups := map[int]bool{}
	seenOfficer := false
	for _, cd := range got[1:] {
		if cd.Empty() {
			continue
		}
		require.True(t, c.Published(cd.TypeID))
		require.NotEqual(t, MetaAbyssal, c.MetaGroup(cd.TypeID))
		groups[c.MetaGroup(cd.TypeID)] = true
		if c.IsOfficer(cd.TypeID) {
			seenOfficer = true
		} else {
			require.False(t, seenOfficer, "type %d follows an Officer type", cd.TypeID)
		}
	}
	t.Logf("HML II candidates: %d, meta groups %v, officer %v", len(got), groups, seenOfficer)
	require.True(t, groups[4] || groups[3] || groups[6], "Faction, Storyline or Deadspace launchers are offered: %v", groups)
	require.Equal(t, 0, got[len(got)-1].TypeID, "empty slot last")
	// The cap keeps the Tech I base type: the cheap launcher is not cut for a Deadspace one.
	require.Contains(t, ids(got), t1, "HML I survives the cap")

	f.High[0].TypeID = navy
	got = c.Candidates(f, hi0, o)
	require.Equal(t, navy, got[0].TypeID)
	var all []int
	for _, cd := range got {
		all = append(all, cd.TypeID)
	}
	require.Contains(t, all, t2, "Tech II is offered to a Faction line")
}
