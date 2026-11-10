package fit_test

// e3a_profiles_test.go - the eight synthetic E3a skill profiles (T4.3).
//
// The profiles are pure data: skill type id -> level maps in testdata/e3a/profiles.json.
// They name no character and carry no real character data. The builder below is the rule
// of section 10 of the lane plan; TestE3a_ProfilesBuiltByRule rebuilds the file from the
// rule on the pinned SDE and fails when the file differs. E3A_UPDATE=1 rewrites the file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

const e3aBuild = "3682781_20261110"

// The six reference hulls and the two extra hulls of the E3a profile table.
var e3aHulls = []struct {
	Name string
	ID   int
}{
	{"Rifter", 587}, {"Vexor", 626}, {"Thorax", 627}, {"Myrmidon", 24700},
	{"Brutix", 16229}, {"Cerberus", 11993}, {"Tengu", 29984}, {"Megathron", 641},
}

type e3aProfileDef struct {
	ID    string
	Shape string // zero | raise | drop | alpha
	Base  int
	Named map[string]int // skill name -> level
}

var e3aProfileDefs = []e3aProfileDef{
	{ID: "all0", Shape: "zero"},
	{ID: "allI", Shape: "raise", Base: 1},
	{ID: "allIII", Shape: "raise", Base: 3},
	{ID: "fitIII", Shape: "drop", Base: 5, Named: map[string]int{
		"CPU Management": 3, "Power Grid Management": 3, "Advanced Weapon Upgrades": 3,
		"Energy Grid Upgrades": 3, "Electronics Upgrades": 3, "Shield Upgrades": 3,
		"Jury Rigging": 3,
		// Named III; Advanced Weapon Upgrades needs Weapon Upgrades IV, so the rule raises it.
		"Weapon Upgrades": 3,
	}},
	{ID: "alpha", Shape: "alpha"},
	{ID: "droner", Shape: "raise", Base: 0, Named: map[string]int{
		"Gallente Cruiser": 4, "Gallente Battlecruiser": 3, "Drones": 4, "Light Drone Operation": 4,
		"Medium Drone Operation": 4, "Drone Navigation": 3, "Drone Avionics": 3, "Weapon Upgrades": 4,
		"CPU Management": 3, "Power Grid Management": 3, "Hull Upgrades": 3, "Mechanics": 3,
		"Repair Systems": 3,
	}},
	{ID: "missiler", Shape: "drop", Base: 5, Named: map[string]int{
		"Heavy Missile Specialization": 0, "Heavy Assault Cruisers": 1, "Warhead Upgrades": 3,
		"Rapid Launch": 4, "Missile Bombardment": 3,
	}},
	{ID: "tanker", Shape: "raise", Base: 0, Named: map[string]int{
		"Hull Upgrades": 4, "Repair Systems": 3, "Mechanics": 4, "EM Armor Compensation": 2,
		"Explosive Armor Compensation": 1, "Kinetic Armor Compensation": 1, "Capacitor Management": 3,
		"Capacitor Systems Operation": 2, "Gallente Battlecruiser": 1,
	}},
}

// e3aProfile is one profile in the JSON file. Skills is complete: every published skill.
type e3aProfile struct {
	Shape   string         `json:"shape"`
	Skills  map[string]int `json:"skills"`
	Raised  map[string]int `json:"raised,omitempty"`  // skill -> level the rule raised it to
	Dropped []int          `json:"dropped,omitempty"` // filler skills sent to 0 (Drop shape)
}

type e3aProfileFile struct {
	Build    string                `json:"build"`
	Profiles map[string]e3aProfile `json:"profiles"`
}

func e3aDir() string { return filepath.Join("testdata", "e3a") }

func e3aOpenSDE(t testing.TB) *sde.SDE {
	t.Helper()
	s, err := sde.Open(sdetest.Path(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// e3aReq is a skill's direct prerequisites, read from its own attributes (182..279, 1285..1290).
func e3aReq(s *sde.SDE, id int) [][2]int {
	a := s.GetDogma(id)
	var out [][2]int
	for _, p := range [][2]int{{182, 277}, {183, 278}, {184, 279}, {1285, 1286}, {1289, 1287}, {1290, 1288}} {
		if v := int(a[p[0]]); v != 0 {
			out = append(out, [2]int{v, int(a[p[1]])})
		}
	}
	return out
}

// e3aBuildProfile applies the rule of section 10 of the plan.
func e3aBuildProfile(t testing.TB, s *sde.SDE, d e3aProfileDef) e3aProfile {
	t.Helper()
	skills := s.GetSkillTypeIDs()
	sort.Ints(skills)
	isSkill := map[int]bool{}
	for _, id := range skills {
		isSkill[id] = true
	}
	named := map[int]int{}
	byName := s.ResolveNames(keysOf(d.Named))
	for n, l := range d.Named {
		id, ok := byName[n]
		require.True(t, ok && isSkill[id], "skill %q must resolve", n)
		named[id] = l
	}
	m := map[int]int{}
	p := e3aProfile{Shape: d.Shape}
	switch d.Shape {
	case "zero":
		for _, id := range skills {
			m[id] = 0
		}
	case "alpha":
		caps, err := s.AlphaCloneSkills()
		require.NoError(t, err)
		for _, id := range skills {
			m[id] = caps[id]
		}
	case "raise":
		for _, id := range skills {
			m[id] = d.Base
		}
		for id, l := range named {
			m[id] = l
		}
		p.Raised = map[string]int{}
		for changed := true; changed; {
			changed = false
			for _, id := range skills {
				if m[id] < 1 {
					continue
				}
				for _, r := range e3aReq(s, id) {
					if m[r[0]] < r[1] {
						m[r[0]] = r[1]
						p.Raised[strconv.Itoa(r[0])] = r[1]
						changed = true
					}
				}
			}
		}
	case "drop":
		// pinned: the named skills and the prerequisites the rule raises; every other skill
		// is the base level, or 0 when a prerequisite is unmet.
		pinned := map[int]int{}
		for id, l := range named {
			pinned[id] = l
		}
		p.Raised = map[string]int{}
		eval := func() (map[int]int, map[int]bool) {
			dropped := map[int]bool{}
			cur := map[int]int{}
			for _, id := range skills {
				cur[id] = d.Base
			}
			for id, l := range pinned {
				cur[id] = l
			}
			for changed := true; changed; {
				changed = false
				for _, id := range skills {
					if _, ok := pinned[id]; ok || cur[id] == 0 {
						continue
					}
					for _, r := range e3aReq(s, id) {
						if cur[r[0]] < r[1] {
							cur[id] = 0
							dropped[id] = true
							changed = true
							break
						}
					}
				}
			}
			return cur, dropped
		}
		var dropped map[int]bool
		for {
			m, dropped = eval()
			changed := false
			for _, id := range skills {
				if m[id] < 1 {
					continue
				}
				if _, ok := pinned[id]; !ok {
					continue
				}
				for _, r := range e3aReq(s, id) {
					if m[r[0]] < r[1] {
						pinned[r[0]] = r[1]
						changed = true
					}
				}
			}
			if !changed {
				break
			}
		}
		for id, l := range pinned {
			if nl, ok := named[id]; !ok || l > nl {
				p.Raised[strconv.Itoa(id)] = l
			}
		}
		for id := range dropped {
			p.Dropped = append(p.Dropped, id)
		}
		sort.Ints(p.Dropped)
	default:
		t.Fatalf("unknown shape %q", d.Shape)
	}
	p.Skills = map[string]int{}
	for _, id := range skills {
		p.Skills[strconv.Itoa(id)] = m[id]
	}
	if len(p.Raised) == 0 {
		p.Raised = nil
	}
	return p
}

func keysOf(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func e3aBuildAll(t testing.TB, s *sde.SDE) e3aProfileFile {
	f := e3aProfileFile{Build: e3aBuild, Profiles: map[string]e3aProfile{}}
	for _, d := range e3aProfileDefs {
		f.Profiles[d.ID] = e3aBuildProfile(t, s, d)
	}
	return f
}

func e3aLoadProfiles(t testing.TB) e3aProfileFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e3aDir(), "profiles.json"))
	require.NoError(t, err)
	var f e3aProfileFile
	require.NoError(t, json.Unmarshal(b, &f))
	return f
}

// e3aSkillProfile converts a stored profile to the engine's SkillProfile.
func e3aSkillProfile(t testing.TB, p e3aProfile) fit.SkillProfile {
	t.Helper()
	out := fit.SkillProfile{}
	for k, v := range p.Skills {
		id, err := strconv.Atoi(k)
		require.NoError(t, err)
		out[id] = v
	}
	return out
}

// TestE3a_ProfilesBuiltByRule: profiles.json equals the rule applied to the pinned SDE.
func TestE3a_ProfilesBuiltByRule(t *testing.T) {
	s := e3aOpenSDE(t)
	want := e3aBuildAll(t, s)
	if os.Getenv("E3A_UPDATE") == "1" {
		b, err := json.MarshalIndent(want, "", " ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(e3aDir(), "profiles.json"), append(b, '\n'), 0o644))
	}
	got := e3aLoadProfiles(t)
	require.Equal(t, e3aBuild, got.Build)
	require.Len(t, got.Profiles, len(e3aProfileDefs))
	for _, d := range e3aProfileDefs {
		require.True(t, reflect.DeepEqual(want.Profiles[d.ID], got.Profiles[d.ID]), "profile %s differs from the rule", d.ID)
	}
}

// TestE3a_ProfilesClosed: no skill above 0 lacks a prerequisite level, in every profile,
// and every profile lists every published skill.
func TestE3a_ProfilesClosed(t *testing.T) {
	s := e3aOpenSDE(t)
	f := e3aLoadProfiles(t)
	skills := s.GetSkillTypeIDs()
	for id, p := range f.Profiles {
		require.Len(t, p.Skills, len(skills), "profile %s lists every published skill", id)
		for _, sk := range skills {
			lvl := p.Skills[strconv.Itoa(sk)]
			require.True(t, lvl >= 0 && lvl <= 5, "profile %s skill %d level %d", id, sk, lvl)
			if lvl == 0 {
				continue
			}
			for _, r := range e3aReq(s, sk) {
				require.GreaterOrEqual(t, p.Skills[strconv.Itoa(r[0])], r[1],
					"profile %s: skill %d at %d lacks prerequisite %d level %d", id, sk, lvl, r[0], r[1])
			}
		}
	}
}

func e3aName(s *sde.SDE, id int) string {
	if n := s.GetTypeName(id); n != nil {
		return *n
	}
	return fmt.Sprint(id)
}
