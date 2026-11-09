package solve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
)

// no_candidate report tests (plan 2026-11-07-dp2-fit-l4-l6, T5.7), real SDE.

func reportFor(t *testing.T, c *Catalog, r Request) (*Result, *Report) {
	t.Helper()
	ev := NewEngineEvaluator(gofa.New(c.Store()), 4)
	res, err := Solve(context.Background(), c, ev, r)
	require.NoError(t, err)
	require.Equal(t, StatusNoCandidate, res.Status)
	rep, err := BuildReport(context.Background(), c, ev, r, res)
	require.NoError(t, err)
	t.Logf("binding %+v; B2 %+v; gpi %d, evals %d, closest %d", rep.Binding, res.Baseline.Flown, len(res.NoCandidate.GatePassingInvalid), res.Evaluations, len(rep.Closest))
	for _, l := range rep.Train {
		t.Logf("train: %s", l.Text)
	}
	return res, rep
}

func skillID(t *testing.T, c *Catalog, name string) int {
	t.Helper()
	id, ok := c.Store().ResolveNames([]string{name})[name]
	require.True(t, ok, name)
	return id
}

// A Megathron for the pilot who has no skills: the hull binds, and the train names the hull's
// skill.
func TestNoCandidate_ImpossibleCase(t *testing.T) {
	c := realCatalog(t)
	f := fit.Fit{HullID: hullMegathron, HullName: "Megathron"}
	res, rep := reportFor(t, c, Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["all0"]})
	require.NotNil(t, res.NoCandidate.Hull)
	require.Equal(t, BindSkill, rep.Binding.Code)
	require.NotNil(t, rep.Binding.Skill)
	require.Contains(t, rep.Binding.Text, "Megathron")
	require.NotEmpty(t, rep.Train)
	require.LessOrEqual(t, len(rep.Train), 3)
	gb := skillID(t, c, "Gallente Battleship")
	found := false
	for _, l := range rep.Train {
		if l.SkillID == gb {
			found = true
			require.Equal(t, "Gallente Battleship", l.Name)
			require.Equal(t, 0, l.From)
			require.Greater(t, l.To, 0)
			require.Contains(t, l.Text, "Gallente Battleship")
		}
	}
	require.True(t, found, "train lists Gallente Battleship: %+v", rep.Train)
	require.Equal(t, TrainNote, rep.Note)
	require.Empty(t, rep.Closest)
}

// The reports below run on techCatalog (Tech I and Tech II modules only, the candidate universe
// before the 2026-11-09 rulings): the wider universe solves these fixtures.
//
// The reference fits under the eight E3a profiles with the fallback policy never end
// no_candidate with gate-passing candidates over the powergrid (scanned 2026-11-09 on build
// 3670638_20261104: every no_candidate pair is a hull the profile cannot fly, or candidates
// whose lines the pilot cannot use). So the fixture is built, as the plan allows: a Rifter
// with 3x 280mm Howitzer Artillery I, 3x Small Shield Extender I, Damage Control I, Small
// Armor Repairer I and 200mm Steel Plates I, under profile fitIII, with the policy
// DPS and EHP at 100 % of B2. Frozen numbers: B2 DPS 92.03, EHP 5860.18, invalid; the
// original passes the gates and breaks the powergrid only, load 52.19 MW of 47.15 MW (short
// by 5.04 MW). The search takes 37 evaluations and keeps 15 gate-passing invalid options (73
// evaluations and 27 options before the twin-line pruning of the repair; the repair keeps the
// equal-need candidates, which can raise the ship's output); the smallest violation among
// them is 47.1905 MW of 47.15 MW, short by 0.0405 MW. The test pins the bounds (the pruned
// search evaluates fewer than the unpruned 73 and keeps no more than its 27 options) and logs
// the exact numbers, so a candidate-list change does not break it for a reason that is not
// the pruning.
const pgFixtureEFT = `[Rifter, PG fixture]
Damage Control I
Small Armor Repairer I
200mm Steel Plates I

Small Shield Extender I
Small Shield Extender I
Small Shield Extender I

280mm Howitzer Artillery I, EMP S
280mm Howitzer Artillery I, EMP S
280mm Howitzer Artillery I, EMP S

Small Projectile Burst Aerator I
Small Projectile Burst Aerator I
`

func pgFixture(t *testing.T, c *Catalog) Request {
	f, un := fit.ParseEFT(pgFixtureEFT, c.Store())
	require.Empty(t, un)
	return Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["fitIII"],
		Policy: Policy{Gates: []Gate{{Stat: StatDPS, AtLeast: 100}, {Stat: StatEHP, AtLeast: 100}},
			Baseline: BaselineFlown, AllowT2toT1: true, From: FromRequest}}
}

func TestNoCandidate_PowergridBinding(t *testing.T) {
	c := techCatalog(t)
	res, rep := reportFor(t, c, pgFixture(t, c))
	require.Nil(t, res.NoCandidate.Hull)
	require.NotEmpty(t, res.NoCandidate.GatePassingInvalid)
	require.False(t, res.Baseline.Flown.Valid)
	b := rep.Binding
	require.Equal(t, BindPG, b.Code)
	require.Equal(t, "pg_over", b.Violation)
	require.Equal(t, "MW", b.Unit)
	require.InDelta(t, 47.15, b.Capacity, 0.01)
	require.InDelta(t, 0.0405, b.Over, 0.001)
	require.InDelta(t, 47.1905, b.Use, 0.001)
	t.Logf("%d gate-passing invalid options, %d evaluations", len(res.NoCandidate.GatePassingInvalid), res.Evaluations)
	require.Greater(t, len(res.NoCandidate.GatePassingInvalid), 0)
	require.LessOrEqual(t, len(res.NoCandidate.GatePassingInvalid), 27)
	require.Less(t, res.Evaluations, 73)
	require.Equal(t, "Powergrid short by 0.04 MW", b.Text)
	require.Contains(t, b.Text, "MW")

	pgm, egu := skillID(t, c, "Power Grid Management"), skillID(t, c, "Energy Grid Upgrades")
	require.NotEmpty(t, rep.Train)
	require.LessOrEqual(t, len(rep.Train), 3)
	named := false
	for _, l := range rep.Train {
		require.Greater(t, l.To, l.From)
		require.LessOrEqual(t, l.To, 5)
		if l.SkillID == pgm || l.SkillID == egu {
			named = true
			require.Contains(t, l.Text, l.Name)
			require.Contains(t, []string{"III", "IV", "V"}, roman(l.To))
		}
	}
	require.True(t, named, "train names PGM or EGU: %+v", rep.Train)
	// Ordered by levels needed, then skill id.
	for i := 1; i < len(rep.Train); i++ {
		a, b := rep.Train[i-1], rep.Train[i]
		require.True(t, a.To-a.From < b.To-b.From || (a.To-a.From == b.To-b.From && a.SkillID < b.SkillID), "%+v", rep.Train)
	}
}

func TestNoCandidate_ClosestAtMostTwo(t *testing.T) {
	c := techCatalog(t)
	res, rep := reportFor(t, c, pgFixture(t, c))
	require.LessOrEqual(t, len(rep.Closest), 2)
	require.Equal(t, res.NoCandidate.Closest[:len(rep.Closest)], rep.Closest)
	require.Len(t, rep.Closest, 2)
	for _, o := range rep.Closest {
		require.True(t, o.Result.Valid)
	}

	// More valid-but-short options than two in the seam still report two.
	nc := *res.NoCandidate
	nc.Closest = append(append([]Option{}, nc.Closest...), nc.Closest...)
	res2 := *res
	res2.NoCandidate = &nc
	rep2, err := BuildReport(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4), pgFixture(t, c), &res2)
	require.NoError(t, err)
	require.Len(t, rep2.Closest, 2)

	// A report needs a no_candidate result.
	_, err = BuildReport(context.Background(), c, nil, Request{}, &Result{Status: StatusOK})
	require.Error(t, err)
}

// M1: the training candidates are ranked by relevance before the cap of ten, not cut to the
// ten lowest skill ids. Cerberus (reference 3) under allIII with a DPS gate at 101 % of Flown:
// the train names a skill that lifts the missile damage (Warhead Upgrades or Rapid Launch).
func TestNoCandidate_TrainRanksRelevantSkills(t *testing.T) {
	c := techCatalog(t)
	f := refFit(t, c, 3)
	r := Request{Kind: KindRefit, Fit: f, Skills: e3aProfiles(t)["allIII"],
		Policy: Policy{Gates: []Gate{{Stat: StatDPS, AtLeast: 101}}, Baseline: BaselineFlown, AllowT2toT1: true, From: FromRequest}}
	_, rep := reportFor(t, c, r)
	wu, rl := skillID(t, c, "Warhead Upgrades"), skillID(t, c, "Rapid Launch")
	named := false
	for _, l := range rep.Train {
		if l.SkillID == wu || l.SkillID == rl {
			named = true
		}
	}
	require.True(t, named, "train names Warhead Upgrades or Rapid Launch: %+v", rep.Train)
}

// M2: a skill binding trains the missing skills, naming the line and the skill, from the
// pilot's level to the required one, the direct requirements first. The Vexor (reference 2)
// carries a Medium Capacitor Booster II the droner pilot cannot use.
func TestNoCandidate_SkillBindingTrains(t *testing.T) {
	c := techCatalog(t)
	f := refFit(t, c, 2)
	droner := e3aProfiles(t)["droner"]
	r := Request{Kind: KindRefit, Fit: f, Skills: droner}
	res, _ := reportFor(t, c, r)
	viols := fit.SkillViolations(context.Background(), c.Store(), f, fit.StatsOpts{Skills: droner, MissingAsZero: true})
	require.NotEmpty(t, viols)
	only := *res
	nc := *res.NoCandidate
	nc.GatePassingInvalid = []Option{{Fit: f, Violations: viols}}
	only.NoCandidate = &nc
	rep, err := BuildReport(context.Background(), c, NewEngineEvaluator(gofa.New(c.Store()), 4), r, &only)
	require.NoError(t, err)
	require.Equal(t, BindSkill, rep.Binding.Code)
	require.NotEmpty(t, rep.Train)
	require.LessOrEqual(t, len(rep.Train), 3)
	// The first line is the first missing skill of the first violation (the direct one).
	first := viols[0]
	require.Equal(t, first.Skill.TypeID, rep.Train[0].SkillID)
	require.Equal(t, first.Skill.Has, rep.Train[0].From)
	require.Equal(t, first.Skill.Needs, rep.Train[0].To)
	require.Equal(t, first.Names[0], rep.Train[0].For)
	require.Contains(t, rep.Train[0].Text, rep.Train[0].Name)
	require.Contains(t, rep.Train[0].Text, first.Names[0])
	seen := map[int]bool{}
	for _, l := range rep.Train {
		require.False(t, seen[l.SkillID], "one line per skill")
		seen[l.SkillID] = true
		require.Greater(t, l.To, l.From)
	}
	require.Contains(t, rep.Binding.Text, first.Names[0])
}
