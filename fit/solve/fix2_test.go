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

// capFake is an Evaluator whose capacitor is stable when the fit has at least minHigh high
// modules. Everything else is zero.
type capFake struct{ minHigh int }

func (e capFake) EvaluateBatch(_ context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	out := make([]Evaluation, len(reqs))
	for i, r := range reqs {
		out[i].Stats.Capacitor.Stable = len(r.Fit.High) >= e.minHigh
	}
	return out, nil
}

func capResult(used string, designedStable bool) *Result {
	return &Result{Baseline: Baselines{
		Designed: Numbers{CapStable: designedStable},
		Flown:    Numbers{CapStable: true},
		Used:     used,
	}}
}

// M2: stability carried by two unusable lines together is found by the all-removed fit.
func TestCapBaselineTrain_TwoUnusableLinesTogether(t *testing.T) {
	c := NewCatalog(testSubset())
	f := highFit(108, 108, 100)
	r := Request{Kind: KindDowngrade, Fit: f, Skills: poor}
	// One line removed leaves 2 high modules (stable), both removed leave 1 (unstable).
	train, note, err := capBaselineTrain(context.Background(), c, capFake{minHigh: 2}, r, capResult(BaselineFlown, true))
	require.NoError(t, err)
	require.NotEmpty(t, train)
	require.Equal(t, skillRare, train[0].SkillID)
	require.Contains(t, note, "cap-stable only through")
	require.Contains(t, note, "cannot use")
}

// M2 on the real SDE: Vexor (reference 2) for the droner with the Afterburner replaced by a
// second Medium Capacitor Booster II. Both boosters keep the fit cap-stable, either alone does
// too, and without both it is unstable: the cap binding names the skill of the boosters.
func TestNoCandidate_CapBindingTwoUnusableBoosters(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 2)
	f.Mid = append([]fit.FitModule(nil), f.Mid...)
	for i, m := range f.Mid {
		if m.Name == "10MN Afterburner II" {
			f.Mid[i] = f.Mid[1]
		}
	}
	r := Request{Kind: KindDowngrade, Fit: f, Skills: e3aProfiles(t)["droner"]}
	_, rep := reportFor(t, c, r)
	require.Equal(t, BindCap, rep.Binding.Code)
	ces := skillID(t, c, "Capacitor Systems Operation")
	found := false
	for _, l := range rep.Train {
		found = found || l.SkillID == ces
	}
	require.True(t, found, "train names Capacitor Systems Operation: %+v", rep.Train)
	require.Contains(t, rep.Note, "cap-stable only through")
}

// L6: the cap rule gates against B2, so the note appears even when the policy baseline is
// the designed one and that baseline is not cap-stable.
func TestCapBaselineTrain_AlwaysChecksFlownBaseline(t *testing.T) {
	c := NewCatalog(testSubset())
	r := Request{Kind: KindDowngrade, Fit: highFit(108, 100), Skills: poor}
	train, note, err := capBaselineTrain(context.Background(), c, capFake{minHigh: 2}, r, capResult(BaselineDesigned, false))
	require.NoError(t, err)
	require.NotEmpty(t, train)
	require.Contains(t, note, "cap-stable only through")
}

// L7: one train line per skill, with the highest target level, then at most 3 lines.
func TestCapBaselineTrain_DedupesBySkill(t *testing.T) {
	c := NewCatalog(testSubset())
	r := Request{Kind: KindDowngrade, Fit: highFit(108, 108, 100), Skills: poor}
	train, _, err := capBaselineTrain(context.Background(), c, capFake{minHigh: 2}, r, capResult(BaselineFlown, true))
	require.NoError(t, err)
	require.Len(t, train, 1, "two items needing the same skill give one line: %+v", train)

	got := dedupeTrain([]TrainLine{
		{SkillID: 1, From: 0, To: 2, Text: "a"}, {SkillID: 2, To: 3}, {SkillID: 1, From: 0, To: 4, Text: "b"}, {SkillID: 3, To: 1},
	})
	require.Len(t, got, 3)
	require.Equal(t, 1, got[0].SkillID)
	require.Equal(t, 4, got[0].To, "highest level kept, at the first position")
	require.Equal(t, 2, got[1].SkillID)
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
