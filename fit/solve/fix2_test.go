package solve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
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

// L5: the skill binding trains to the highest level any line needs.
func TestSkillBinding_KeepsHighestLevel(t *testing.T) {
	c := NewCatalog(testSubset())
	opt := Option{Violations: []fit.Violation{
		{Code: fit.CodeSkill, Names: []string{"A"}, Skill: &fit.MissingSkill{TypeID: skillRare, Has: 1, Needs: 2}},
		{Code: fit.CodeSkill, Names: []string{"B"}, Skill: &fit.MissingSkill{TypeID: skillRare, Has: 1, Needs: 4}},
	}}
	text, train := c.skillBinding(Binding{Code: BindSkill}, opt)
	require.Len(t, train, 1)
	require.Equal(t, 4, train[0].To)
	require.Equal(t, 1, train[0].From)
	require.Contains(t, text, "IV")
}
