package solve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// Owner ruling 2026-11-09: Downgrade of the Rifter (reference 1) under fitIII returns a valid
// fit; the old rule (never to a higher meta group) is dropped, so a move up is allowed when it
// helps the objective, an Officer module is not introduced.
func TestSolve_RifterFitIIIDowngradeReturnsValidFit(t *testing.T) {
	c := realCatalog(t)
	sk := e3aProfiles(t)["fitIII"]
	f := refFit(t, c, 1)
	ev := NewEngineEvaluator(gofa.New(c.Store()), 2)
	res, err := Solve(context.Background(), c, ev, Request{Kind: KindDowngrade, Fit: f, Skills: sk})
	require.NoError(t, err)
	require.NotEqual(t, StatusNoCandidate, res.Status)
	require.NotEmpty(t, res.Changes)
	require.True(t, res.Result.Valid)
	for _, ch := range res.Changes {
		require.False(t, ch.To != 0 && c.IsOfficer(ch.To), "%s introduces an Officer module", c.nameOf(ch.To))
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

// Vexor (reference 2) for the droner with the Afterburner replaced by a second Medium
// Capacitor Booster II (Downgrade). Owner ruling 2026-11-09 (4a): the cap-booster lines the
// pilot cannot use no longer end the solve with a capacitor binding; a result comes back with
// a capacitor warning that names Capacitor Systems Operation, the skill of the boosters.
func TestSolve_VexorTwoUnusableBoostersWarnsCapacitor(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 2)
	f.Mid = append([]fit.FitModule(nil), f.Mid...)
	for i, m := range f.Mid {
		if m.Name == "10MN Afterburner II" {
			f.Mid[i] = f.Mid[1]
		}
	}
	r := Request{Kind: KindDowngrade, Fit: f, Skills: e3aProfiles(t)["droner"]}
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4), r)
	require.NoError(t, err)
	requireCapacitorWarning(t, c, res)
}

// requireCapacitorWarning: the result is a fit (not no_candidate), the baseline is cap-stable,
// the chosen fit is not, and the warning names Capacitor Systems Operation (3417).
func requireCapacitorWarning(t *testing.T, c *Catalog, res *Result) {
	t.Helper()
	require.Contains(t, []Status{StatusOK, StatusOKWithDowngrades, StatusUncertain}, res.Status)
	require.NotNil(t, res.Fit)
	require.True(t, res.Result.Valid)
	require.True(t, res.Baseline.Flown.CapStable)
	require.False(t, res.Result.CapStable)
	var w *Warning
	for i := range res.Warnings {
		if res.Warnings[i].Code == WarnCapacitor {
			w = &res.Warnings[i]
		}
	}
	require.NotNil(t, w, "capacitor warning: %+v", res.Warnings)
	require.NotEmpty(t, w.Train)
	require.LessOrEqual(t, len(w.Train), 3)
	ces := skillID(t, c, "Capacitor Systems Operation") // 3417
	found := false
	for _, l := range w.Train {
		if l.SkillID == ces {
			found = true
			require.Greater(t, l.To, l.From)
		}
	}
	require.True(t, found, "train names Capacitor Systems Operation: %+v", w.Train)
	require.Contains(t, w.Message, "not stable")
}

// Vexor, reference 2, droner, Downgrade as written: the pilot cannot use the Medium Capacitor
// Booster II; the same warning.
func TestSolve_VexorDronerDowngradeWarnsCapacitor(t *testing.T) {
	c := realCatalog(t)
	r := Request{Kind: KindDowngrade, Fit: refFit(t, c, 2), Skills: e3aProfiles(t)["droner"]}
	res, err := Solve(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4), r)
	require.NoError(t, err)
	t.Logf("%s, %d changes, warnings %+v", res.Status, len(res.Changes), res.Warnings)
	requireCapacitorWarning(t, c, res)
	b, _ := json.Marshal(res)
	var again Result
	require.NoError(t, json.Unmarshal(b, &again))
	require.Equal(t, res.Warnings, again.Warnings)
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

// M3: a written type outside the subset survives through the start probe unless judge rejects
// every assignment that holds one. Rifter reference 1 with Mid[2] a type the subset lacks.
func TestSolve_OutsideSubsetStartIsNeverReturned(t *testing.T) {
	c := realCatalog(t)
	f := refFit(t, c, 1)
	f.Mid = append([]fit.FitModule(nil), f.Mid...)
	f.Mid[2] = fit.FitModule{TypeID: 99999991, Name: "Not In Subset", Qty: 1}
	ev := NewEngineEvaluator(gofa.New(c.Store()), 2)
	for _, k := range []Kind{KindRefit, KindDowngrade} {
		res, err := Solve(context.Background(), c, ev, Request{Kind: k, Fit: f, Skills: nil})
		require.NoError(t, err)
		if res.Fit != nil {
			for _, m := range res.Fit.Mid {
				require.NotEqual(t, 99999991, m.TypeID, "%s keeps a type outside the subset", k)
			}
		}
	}
}

// M3 synthetic: a start whose other lines are valid, with one type outside the subset.
func TestSolve_OutsideSubsetTypeNeverRanksBest(t *testing.T) {
	sub := fakeSubset(gun(1000, 1, 0, 40, nil), gun(1002, 1, 3, 49.5, nil))
	f := highFit(1000, 7777)
	for _, k := range []Kind{KindRefit, KindDowngrade} {
		res := fakeSolve(t, sub, Request{Kind: k, Fit: f, Skills: poor})
		if res.Fit != nil {
			require.NotContains(t, highTypes(res.Fit), 7777, k)
		}
	}
}

// L4: a charge outside the subset does not ride along in the forced phase of a line with
// several candidates.
func TestSolve_OutsideSubsetChargeNeverRidesAlong(t *testing.T) {
	sub := fakeSubset(gun(1000, 1, 0, 40, nil), gun(1001, 2, 5, 50, rare(map[int]float64{})), gun(1002, 1, 3, 49.5, nil))
	f := highFit(1001, 1000)
	f.High[1].ChargeID = 8888
	res := fakeSolve(t, sub, Request{Kind: KindRefit, Fit: f, Skills: poor})
	require.NotEqual(t, StatusNoCandidate, res.Status)
	require.NotNil(t, res.Fit)
	for _, m := range res.Fit.High {
		require.NotEqual(t, 8888, m.ChargeID)
	}
}

// R3 L1: the text and the train line name the line whose violation has the top level.
func TestSkillBinding_NamesTopLevelLine(t *testing.T) {
	c := NewCatalog(testSubset())
	opt := Option{Violations: []fit.Violation{
		{Code: fit.CodeSkill, Names: []string{"A"}, Skill: &fit.MissingSkill{TypeID: skillRare, Has: 1, Needs: 2}},
		{Code: fit.CodeSkill, Names: []string{"B"}, Skill: &fit.MissingSkill{TypeID: skillRare, Has: 1, Needs: 4}},
		{Code: fit.CodeSkill, Names: []string{"C"}, Skill: &fit.MissingSkill{TypeID: skillRare, Has: 1, Needs: 4}},
	}}
	text, train := c.skillBinding(Binding{Code: BindSkill}, opt)
	require.Len(t, train, 1)
	require.Equal(t, "B", train[0].For, "first line among ties")
	require.Contains(t, train[0].Text, "(B)")
	require.True(t, strings.HasPrefix(text, "B needs"), text)
}

// R3 L2: a probe rejected for a type outside the subset carries a hard violation, so it never
// reaches the gate-passing-invalid list without one and the report names the item.
func TestSolve_OutsideSubsetRejectionHasViolation(t *testing.T) {
	sub := fakeSubset(gun(1000, 1, 0, 40, nil), gun(1002, 1, 3, 49.5, nil))
	c := NewCatalog(sub)
	ev := &fakeEngine{store: c.Store(), workers: 1}
	for _, k := range []Kind{KindRefit, KindDowngrade} {
		r := Request{Kind: k, Fit: highFit(1000, 7777), Skills: poor, MaxEvaluations: 2}
		res, err := Solve(context.Background(), c, ev, r)
		require.NoError(t, err)
		require.NotNil(t, res.NoCandidate, k)
		for _, o := range res.NoCandidate.GatePassingInvalid {
			require.NotEmpty(t, o.Violations, "%s: invalid option without a violation", k)
		}
		rep, err := BuildReport(context.Background(), c, ev, r, res)
		require.NoError(t, err)
		require.Equal(t, BindOther, rep.Binding.Code, k)
		require.Contains(t, rep.Binding.Text, "t7777", k)
		require.NotContains(t, rep.Binding.Text, "baseline", k)
	}
}

// countFake counts the requests it is given; the capacitor is stable with at least minHigh high modules.
type countFake struct {
	minHigh int
	n       *int
}

func (e countFake) EvaluateBatch(_ context.Context, reqs []gofa.Request) ([]Evaluation, error) {
	*e.n += len(reqs)
	return capFake{minHigh: e.minHigh}.EvaluateBatch(context.Background(), reqs)
}

// R3 L3: with two unusable lines, "all removed" is the single pair: n singles + 1 pair.
func TestCapBaselineTrain_TwoLinesNoDuplicateEvaluation(t *testing.T) {
	c := NewCatalog(testSubset())
	r := Request{Kind: KindDowngrade, Fit: highFit(108, 108, 100), Skills: poor}
	n := 0
	train, _, err := capBaselineTrain(context.Background(), c, countFake{minHigh: 2, n: &n}, r, capResult(BaselineFlown, true))
	require.NoError(t, err)
	require.NotEmpty(t, train)
	require.Equal(t, 3, n, "2 singles + 1 pair")
}
