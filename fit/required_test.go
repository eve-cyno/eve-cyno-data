package fit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// fakeReq is a RequiredSDE over a literal table: type → list of (skill, level).
type fakeReq map[int][][2]int

func (f fakeReq) GetDogma(id int) map[int]float64 {
	m := map[int]float64{}
	for i, p := range f[id] {
		m[requiredSkillPairs[i][0]] = float64(p[0])
		m[requiredSkillPairs[i][1]] = float64(p[1])
	}
	return m
}

func openReqSDE(t *testing.T) *sde.SDE {
	t.Helper()
	s, err := sde.Open(sdetest.Path(t))
	if err != nil || !s.Available() {
		sdetest.Skip(t, "data/sde/sde.sqlite not available; skipping real-SDE test")
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func levels(r []SkillLevelReq) map[int]int {
	m := map[int]int{}
	for _, q := range r {
		m[q.SkillTypeID] = q.Level
	}
	return m
}

// Heavy Missile Launcher II (2410), SDE build 3670638_20261104. Direct: Missile
// Launcher Operation (3319) IV and Heavy Missile Specialization (20211) I. The
// chain: Specialization needs MLO I and Heavy Missiles (3324) V; Heavy Missiles
// needs MLO III and Light Missiles (3321) III; Light Missiles needs MLO II.
// Public source for the skill tree: EVE University, "Heavy Missile Launcher II"
// and the in-game Show Info of the skills (the rows here are read from the SDE).
func TestRequiredSkills_HeavyMissileLauncherII(t *testing.T) {
	s := openReqSDE(t)
	r := RequiredSkills(context.Background(), s, []int{2410})[0]
	require.Equal(t, []SkillLevelReq{{3319, 4}, {20211, 1}}, r.Direct)
	require.Equal(t, []SkillLevelReq{{3319, 4}, {20211, 1}, {3324, 5}, {3321, 3}}, r.All)
}

func TestRequiredSkills_CycleSafe(t *testing.T) {
	f := fakeReq{
		1:  {{10, 2}},
		10: {{11, 3}},
		11: {{10, 1}, {12, 4}}, // 10 -> 11 -> 10
		12: {{12, 5}},          // self loop
	}
	r := RequiredSkills(context.Background(), f, []int{1})[0]
	require.Equal(t, []SkillLevelReq{{10, 2}, {11, 3}, {12, 5}}, r.All, "cycle ends; 12 needs itself at V, the highest level wins")
	// The highest level wins across paths: 10 needed at 2 directly and 1 via 11.
	f2 := fakeReq{1: {{10, 1}, {11, 1}}, 11: {{10, 4}}}
	r2 := RequiredSkills(context.Background(), f2, []int{1})[0]
	require.Equal(t, map[int]int{10: 4, 11: 1}, levels(r2.All))
	require.Equal(t, 10, r2.All[0].SkillTypeID, "direct requirements first")
}

func TestCanUse_AllVAlwaysOK(t *testing.T) {
	s := openReqSDE(t)
	require.True(t, CanUse(context.Background(), s, nil, 2410).OK, "nil profile is all V")
	allV := SkillProfile{3319: 5, 20211: 5, 3324: 5, 3321: 5}
	require.True(t, CanUse(context.Background(), s, allV, 2410).OK)
}

func TestCanUse_AllZeroListsMissing(t *testing.T) {
	s := openReqSDE(t)
	// An empty non-nil profile: every skill missing is 0, never V.
	res := CanUse(context.Background(), s, SkillProfile{}, 2410)
	require.False(t, res.OK)
	require.Equal(t, []MissingSkill{{3319, 4, 0}, {20211, 1, 0}, {3324, 5, 0}, {3321, 3, 0}}, res.Missing)
}

func TestCanUse_HighestLevelWins(t *testing.T) {
	// MLO is needed at I, II, III and IV along four paths: IV is the one reported, once.
	s := openReqSDE(t)
	res := CanUse(context.Background(), s, SkillProfile{3324: 5, 3321: 3, 20211: 1, 3319: 3}, 2410)
	require.Equal(t, []MissingSkill{{3319, 4, 3}}, res.Missing)
	require.False(t, res.OK)
	require.True(t, CanUse(context.Background(), s, SkillProfile{3324: 5, 3321: 3, 20211: 1, 3319: 4}, 2410).OK)
}

// Cerberus (11993): Caldari Cruiser (3334) V and Heavy Assault Cruisers (16591) I.
func TestCanUse_CerberusNeedsCaldariCruiserVAndHAC(t *testing.T) {
	s := openReqSDE(t)
	r := RequiredSkills(context.Background(), s, []int{11993})[0]
	require.Equal(t, []SkillLevelReq{{3334, 5}, {16591, 1}}, r.Direct)
	res := CanUse(context.Background(), s, SkillProfile{}, 11993)
	require.Equal(t, MissingSkill{3334, 5, 0}, res.Missing[0])
	require.Equal(t, MissingSkill{16591, 1, 0}, res.Missing[1])
	// Everything in the closure at its level except HAC: only HAC is reported.
	p := SkillProfile{}
	for _, q := range r.All {
		p[q.SkillTypeID] = q.Level
	}
	delete(p, 16591)
	res = CanUse(context.Background(), s, p, 11993)
	require.Equal(t, []MissingSkill{{16591, 1, 0}}, res.Missing)
}

func corpus27(t *testing.T, s *sde.SDE) []Fit {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "gofa", "corpus.json"))
	require.NoError(t, err)
	var corpus []struct{ Name, EFT string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	var eft []string
	for _, c := range corpus {
		eft = append(eft, c.EFT)
	}
	files, err := filepath.Glob(filepath.Join("testdata", "qc2", "*.eft"))
	require.NoError(t, err)
	for _, p := range files {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		eft = append(eft, string(b))
	}
	require.Len(t, eft, 27)
	var out []Fit
	for _, e := range eft {
		f, _ := ParseEFT(e, s)
		out = append(out, f)
	}
	return out
}

func TestSkillViolations_AllVNone(t *testing.T) {
	s := openReqSDE(t)
	allV := SkillProfile{}
	for _, id := range s.GetSkillTypeIDs() {
		allV[id] = 5
	}
	for i, f := range corpus27(t, s) {
		require.Empty(t, SkillViolations(context.Background(), s, f, StatsOpts{Skills: allV, MissingAsZero: true}), "fit %d", i)
		require.Empty(t, SkillViolations(context.Background(), s, f, StatsOpts{}), "nil profile, fit %d", i)
		require.Empty(t, SkillViolations(context.Background(), s, f, StatsOpts{Skills: SkillProfile{}}), "empty profile without MissingAsZero is V, fit %d", i)
	}
}

func TestSkillViolations_AllZeroCerberus(t *testing.T) {
	s := openReqSDE(t)
	b, err := os.ReadFile(filepath.Join("testdata", "qc2", "q200_cerberus.eft"))
	require.NoError(t, err)
	f, _ := ParseEFT(string(b), s)
	require.Equal(t, 11993, f.HullID)
	v := SkillViolations(context.Background(), s, f, StatsOpts{Skills: SkillProfile{}, MissingAsZero: true})
	require.NotEmpty(t, v)
	for _, x := range v {
		require.Equal(t, Hard, x.Severity)
		require.Equal(t, CodeSkill, x.Code)
		require.Zero(t, x.Skill.Has)
		require.Positive(t, x.Skill.Needs)
	}
	// The hull's own rows come first: Caldari Cruiser V, then HAC I.
	require.Equal(t, 11993, v[0].ItemTypeID)
	require.Equal(t, MissingSkill{3334, 5, 0}, *v[0].Skill)
	require.Equal(t, MissingSkill{16591, 1, 0}, *v[1].Skill)
	// A module's skill shows up too, and cargo does not matter: every item once.
	items := map[int]bool{}
	for _, x := range v {
		items[x.ItemTypeID] = true
	}
	require.Greater(t, len(items), 1)
	// Train Caldari Cruiser V and HAC I and everything under them: the hull rows go.
	p := SkillProfile{}
	for _, q := range RequiredSkills(context.Background(), s, []int{11993})[0].All {
		p[q.SkillTypeID] = q.Level
	}
	for _, x := range SkillViolations(context.Background(), s, f, StatsOpts{Skills: p, MissingAsZero: true}) {
		require.NotEqual(t, 11993, x.ItemTypeID)
	}
}
