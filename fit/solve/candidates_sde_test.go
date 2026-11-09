package solve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// The plan's example on the pinned build: HML II's candidates hold no Faction, Storyline or
// Officer launcher; a Caldari Navy line keeps its own type and may take the other Faction
// launcher of its meta group.
func TestCandidates_AllowedMetaGroups_RealSDE(t *testing.T) {
	s, err := realsde.Open(sdetest.Path(t))
	if err != nil || !s.Available() {
		sdetest.Skip(t, "data/sde/sde.sqlite not available; skipping real-SDE test")
	}
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	c := NewCatalog(sub)
	r := c.Store().ResolveNames([]string{"Heavy Missile Launcher II", "Caldari Navy Heavy Missile Launcher", "Dread Guristas Heavy Missile Launcher"})
	require.Len(t, r, 3)
	t2, navy, dread := r["Heavy Missile Launcher II"], r["Caldari Navy Heavy Missile Launcher"], r["Dread Guristas Heavy Missile Launcher"]

	o := Options{Kind: KindRefit, Skills: nil, Policy: Fallback(KindRefit)}
	f := fit.Fit{HullID: 24690, High: []fit.FitModule{{TypeID: t2, Qty: 1}}} // Drake
	got := c.Candidates(f, hi0, o)
	require.Equal(t, t2, got[0].TypeID)
	require.LessOrEqual(t, len(got), MaxCandidates)
	for _, cd := range got[1:] {
		if cd.Empty() {
			continue
		}
		require.Contains(t, []int{MetaTech1, MetaTech2}, c.MetaGroup(cd.TypeID), "type %d", cd.TypeID)
		require.True(t, c.Published(cd.TypeID))
	}

	f.High[0].TypeID = navy
	got = c.Candidates(f, hi0, o)
	require.Equal(t, navy, got[0].TypeID)
	var all []int
	for _, cd := range got {
		all = append(all, cd.TypeID)
	}
	require.Contains(t, all, dread, "same meta group as the original")
}
