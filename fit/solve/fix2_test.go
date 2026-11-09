package solve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit/gofa"
)

// M1: Downgrade may move a usable line to a cheaper variant of its own meta group (Compact and
// other meta 1-4 types are Tech I like the plain one); it never moves to a higher meta group.
func TestSolve_RifterFitIIIDowngradeSameMetaGroup(t *testing.T) {
	c := realCatalog(t)
	sk := e3aProfiles(t)["fitIII"]
	f := refFit(t, c, 1)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 2)
	res, err := Solve(context.Background(), c, ev, Request{Kind: KindDowngrade, Fit: f, Skills: sk})
	require.NoError(t, err)
	require.NotEqual(t, StatusNoCandidate, res.Status)
	require.NotEmpty(t, res.Changes)
	for _, ch := range res.Changes {
		if ch.To == 0 || !c.CanUse(sk, ch.From) {
			continue
		}
		require.LessOrEqual(t, c.MetaGroup(ch.To), c.MetaGroup(ch.From),
			"%s -> %s moves to a higher meta group", c.nameOf(ch.From), c.nameOf(ch.To))
	}
}
