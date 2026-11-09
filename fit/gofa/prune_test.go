package gofa

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
)

// Skill pruning (plan 2026-11-07-dp2-fit-l4-l6, T5.1, section 5). Real-SDE tests on the
// pinned build; a pruned evaluation must equal the full one exactly.

// pruneProfiles are the three E3a profiles the pruning tests run: every skill at 0 (a
// pruned skill is absent where the full run has it at 0), a partial drop and a
// missile-heavy drop.
var pruneProfiles = []string{"all0", "fitIII", "missiler"}

// loadE3aProfiles reads the E3a skill profiles (testdata/e3a/profiles.json of the fit package).
func loadE3aProfiles(t testing.TB, ids ...string) map[string]fit.SkillProfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "e3a", "profiles.json"))
	require.NoError(t, err)
	var file struct {
		Profiles map[string]struct {
			Skills map[string]int `json:"skills"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	out := map[string]fit.SkillProfile{}
	for _, id := range ids {
		p, ok := file.Profiles[id]
		require.True(t, ok, "profile %q", id)
		sp := fit.SkillProfile{}
		for k, v := range p.Skills {
			n, err := strconv.Atoi(k)
			require.NoError(t, err)
			sp[n] = v
		}
		out[id] = sp
	}
	return out
}

// moduleChoices lists the published modules of each slot, in a stable order.
func moduleChoices(t testing.TB, s *realsde.SDE) map[string][]int {
	t.Helper()
	out := map[string][]int{}
	for _, slot := range []string{"high", "mid", "low", "rig"} {
		for _, h := range s.SearchItems(realsde.ItemSearch{Slot: slot}) {
			out[slot] = append(out[slot], h.TypeID)
		}
		require.NotEmpty(t, out[slot], "no published %s modules", slot)
	}
	return out
}

// randomFit replaces 1-4 module lines of base with random published modules of the
// same slot. A replaced high slot loses its charge (the charge may not fit the new module).
func randomFit(rng *rand.Rand, base fit.Fit, choices map[string][]int) fit.Fit {
	f := base
	f.High = append([]fit.FitModule(nil), base.High...)
	f.Mid = append([]fit.FitModule(nil), base.Mid...)
	f.Low = append([]fit.FitModule(nil), base.Low...)
	f.Rig = append([]fit.FitModule(nil), base.Rig...)
	type ref struct {
		lines *[]fit.FitModule
		slot  string
	}
	var refs []ref
	for _, r := range []ref{{&f.High, "high"}, {&f.Mid, "mid"}, {&f.Low, "low"}, {&f.Rig, "rig"}} {
		if len(*r.lines) > 0 {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		return f
	}
	for n := 1 + rng.Intn(4); n > 0; n-- {
		r := refs[rng.Intn(len(refs))]
		i := rng.Intn(len(*r.lines))
		m := (*r.lines)[i]
		m.TypeID = choices[r.slot][rng.Intn(len(choices[r.slot]))]
		m.Name = ""
		if r.slot == "high" {
			m.ChargeID = 0
		}
		(*r.lines)[i] = m
	}
	return f
}

type pruneCase struct {
	f       fit.Fit
	profile string
}

// forEachParallel runs fn for every index on 4 goroutines.
func forEachParallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

type evalOut struct {
	st  fit.FitStats
	err string
}

func evalOf(e *Engine, f fit.Fit, o fit.StatsOpts) evalOut {
	st, err := e.Stats(context.Background(), f, o)
	if err != nil {
		return evalOut{err: err.Error()}
	}
	return evalOut{st: st}
}

// Pruned equals full on random fits, three profiles, exact.
func TestPrune_EqualsFull(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	full := New(s)
	pruned := New(s).WithPruning()
	profiles := loadE3aProfiles(t, pruneProfiles...)
	bases := loadPerfFits(t, s)
	choices := moduleChoices(t, s)

	n := pruneFits
	if testing.Short() {
		n = 40
	}
	rng := rand.New(rand.NewSource(20261109))
	cases := make([]pruneCase, 0, n*len(pruneProfiles))
	for i := 0; i < n; i++ {
		f := randomFit(rng, bases[rng.Intn(len(bases))].fit, choices)
		for _, p := range pruneProfiles {
			cases = append(cases, pruneCase{f, p})
		}
	}
	bad := make([]string, len(cases))
	forEachParallel(len(cases), func(i int) {
		c := cases[i]
		o := fit.StatsOpts{Skills: profiles[c.profile]}
		a, b := evalOf(full, c.f, o), evalOf(pruned, c.f, o)
		if !reflect.DeepEqual(a, b) {
			bad[i] = "case " + strconv.Itoa(i) + " profile " + c.profile + " hull " + strconv.Itoa(c.f.HullID)
		}
	})
	for i, b := range bad {
		if b != "" {
			t.Errorf("%s: pruned differs from full\nfit: %+v", b, cases[i].f)
			break
		}
	}
}

// The rule really prunes: well under half of the skills stay for a plain fit.
func TestPrune_DropsMostSkills(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	e := New(s)
	ix := skillIndexFor(e.reader())
	var fitTypes []int
	for _, pf := range loadPerfFits(t, s, "qc2/q148_hawk.eft") {
		fitTypes = append(fitTypes, pf.fit.HullID, dogma.CharacterTypeID)
		for _, m := range append(append(append([]fit.FitModule{}, pf.fit.High...), pf.fit.Mid...), pf.fit.Low...) {
			fitTypes = append(fitTypes, m.TypeID)
		}
	}
	kept := ix.keep(e.reader(), fitTypes)
	t.Logf("kept %d of %d skills", len(kept), len(ix.skills))
	require.Less(t, len(kept), len(ix.skills)/2)
	require.Greater(t, len(kept), 0)
}

// A skill at level 0 gives the same result as an absent skill, with one exception the
// plan did not state: a hull skill (Amarr Cruiser, ...) carries its level onto the hull
// with PreMul shipID modifiers, so level 0 multiplies by 0 and absent leaves the hull's own
// default. The rule keeps every such skill (a shipID ItemModifier), so pruning never
// produces that absent case; the test asserts both halves. Found on Maller + 3335.
func TestPrune_LevelZeroEqualsAbsent(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	full := New(s)
	bases := loadPerfFits(t, s)
	choices := moduleChoices(t, s)
	ids := s.GetSkillTypeIDs()
	rng := rand.New(rand.NewSource(7))

	// Hull skills: a PreMul shipID modifier reads the skill level (attribute 280).
	ix := skillIndexFor(full.reader())
	hullSkill := map[int]bool{}
	for _, id := range ids {
		for _, eff := range s.GetTypeEffectIDs(id) {
			for _, m := range s.GetEffectModifiers(eff) {
				if m.Domain == "shipID" && m.Func == "ItemModifier" && m.Operation == opPreMul && m.ModifyingAttr == attrSkillLevel {
					hullSkill[id] = true
				}
			}
		}
	}
	require.NotEmpty(t, hullSkill)
	for id := range hullSkill {
		require.Contains(t, ix.always, id, "hull skill %d must never be pruned", id)
	}

	n := 30
	if testing.Short() {
		n = 8
	}
	type zc struct {
		f    fit.Fit
		zero []int
	}
	cases := make([]zc, n)
	for i := range cases {
		f := randomFit(rng, bases[rng.Intn(len(bases))].fit, choices)
		// Zero the skills this fit needs (the kept ones), a random 12 of them plus 12 unrelated.
		var types []int
		types = append(types, f.HullID, dogma.CharacterTypeID)
		for _, m := range append(append(append([]fit.FitModule{}, f.High...), f.Mid...), f.Low...) {
			types = append(types, m.TypeID)
		}
		kept := skillIndexFor(full.reader()).keep(full.reader(), types)
		var pool []int
		for _, id := range ids {
			if kept[id] {
				pool = append(pool, id)
			}
		}
		rng.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		z := append([]int(nil), pool[:12]...)
		for k := 0; k < 12; k++ {
			z = append(z, ids[rng.Intn(len(ids))])
		}
		var zz []int
		for _, id := range z {
			if !hullSkill[id] {
				zz = append(zz, id)
			}
		}
		cases[i] = zc{f, zz}
	}
	bad := make([]string, n)
	forEachParallel(n, func(i int) {
		c := cases[i]
		zero := map[int]bool{}
		p0, pAbsent := fit.SkillProfile{}, fit.SkillProfile{}
		for _, id := range ids {
			p0[id], pAbsent[id] = 5, 5
		}
		for _, id := range c.zero {
			zero[id] = true
			p0[id] = 0
		}
		absent := *full
		absent.filter = func(_ SDE, _ []int, skills map[int]int) {
			for id := range zero {
				delete(skills, id)
			}
		}
		a := evalOf(full, c.f, fit.StatsOpts{Skills: p0})
		b := evalOf(&absent, c.f, fit.StatsOpts{Skills: pAbsent})
		if !reflect.DeepEqual(a, b) {
			bad[i] = "case " + strconv.Itoa(i)
		}
	})
	for i, b := range bad {
		if b != "" {
			t.Errorf("%s: level 0 differs from absent; zeroed %v\nfit: %+v", b, cases[i].zero, cases[i].f)
			break
		}
	}
}
