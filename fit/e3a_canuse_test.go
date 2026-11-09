package fit_test

// e3a_canuse_test.go - TestE3a_CanUseClaims (T4.3): every "flies" / "cannot use" claim of
// section 10 of the lane plan (the profile table and the worked-pair table), asserted with
// fit.CanUse on the pinned SDE. It runs before T5.8 starts; a failed claim corrects the
// plan first, never the test.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

func e3aHullID(t testing.TB, name string) int {
	t.Helper()
	for _, h := range e3aHulls {
		if h.Name == name {
			return h.ID
		}
	}
	t.Fatalf("unknown hull %s", name)
	return 0
}

// e3aLoadFit parses fixture refN.eft (N = 1..6) through the SDE name resolver.
func e3aLoadFit(t testing.TB, s *sde.SDE, n int) fit.Fit {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e3aDir(), "ref"+strconv.Itoa(n)+".eft"))
	require.NoError(t, err)
	f, unresolved := fit.ParseEFT(string(b), s)
	require.Empty(t, unresolved, "every line of reference %d resolves", n)
	return f
}

// e3aFitItems lists every distinct type id of a fit (hull excluded), with its name.
func e3aFitItems(f fit.Fit) map[int]string {
	out := map[int]string{}
	for _, g := range [][]fit.FitModule{f.High, f.Mid, f.Low, f.Rig, f.Subsystem, f.Drones} {
		for _, m := range g {
			out[m.TypeID] = m.Name
			if m.ChargeID != 0 {
				out[m.ChargeID] = m.Charge
			}
		}
	}
	return out
}

func e3aCanUse(s *sde.SDE, p e3aProfile, t testing.TB, id int) fit.CanUseResult {
	return fit.CanUse(context.Background(), s, e3aSkillProfile(t, p), id)
}

func e3aMissingNames(s *sde.SDE, r fit.CanUseResult) map[string]int {
	out := map[string]int{}
	for _, m := range r.Missing {
		out[e3aName(s, m.TypeID)] = m.Needs
	}
	return out
}

func e3aUnusable(t testing.TB, s *sde.SDE, p e3aProfile, ref int) []string {
	f := e3aLoadFit(t, s, ref)
	var names []string
	for id, n := range e3aFitItems(f) {
		if !e3aCanUse(s, p, t, id).OK {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func TestE3a_CanUseClaims(t *testing.T) {
	s := e3aOpenSDE(t)
	pf := e3aLoadProfiles(t)
	flies := func(profile, hull string) bool {
		return e3aCanUse(s, pf.Profiles[profile], t, e3aHullID(t, hull)).OK
	}
	allHulls := []string{"Rifter", "Vexor", "Thorax", "Myrmidon", "Brutix", "Cerberus", "Tengu", "Megathron"}
	fliesAll := func(profile string, except ...string) {
		t.Helper()
		ex := map[string]bool{}
		for _, h := range except {
			ex[h] = true
		}
		for _, h := range allHulls {
			require.Equal(t, !ex[h], flies(profile, h), "%s x %s", profile, h)
		}
	}
	fliesOnly := func(profile string, hulls ...string) {
		t.Helper()
		ok := map[string]bool{}
		for _, h := range hulls {
			ok[h] = true
		}
		for _, h := range allHulls {
			if ok[h] {
				require.True(t, flies(profile, h), "%s must fly %s", profile, h)
			}
		}
	}

	// profile table
	t.Run("all0 flies none", func(t *testing.T) { fliesAll("all0", allHulls...) })
	t.Run("allI flies all", func(t *testing.T) { fliesAll("allI") })
	t.Run("allIII flies all", func(t *testing.T) { fliesAll("allIII") })
	t.Run("fitIII", func(t *testing.T) {
		fliesAll("fitIII", "Cerberus", "Tengu")
		p := pf.Profiles["fitIII"]
		require.Len(t, p.Dropped, 56, "56 skills drop to 0")
		dropped := map[string]bool{}
		for _, id := range p.Dropped {
			dropped[e3aName(s, id)] = true
		}
		require.True(t, dropped["Heavy Assault Cruisers"])
		for _, n := range []string{"Amarr Core Systems", "Caldari Core Systems", "Gallente Core Systems", "Minmatar Core Systems"} {
			require.True(t, dropped[n], "%s drops", n)
		}
		// Weapon Upgrades III is raised to IV (Advanced Weapon Upgrades needs it).
		wu := s.ResolveNames([]string{"Weapon Upgrades"})["Weapon Upgrades"]
		require.Equal(t, 4, p.Skills[strconv.Itoa(wu)])
		require.Equal(t, 4, p.Raised[strconv.Itoa(wu)])
	})
	t.Run("alpha", func(t *testing.T) { fliesAll("alpha", "Cerberus", "Tengu") })
	t.Run("droner", func(t *testing.T) {
		fliesOnly("droner", "Vexor", "Thorax", "Myrmidon", "Brutix")
		p := pf.Profiles["droner"]
		raised := map[string]int{}
		for k, v := range p.Raised {
			id, _ := strconv.Atoi(k)
			raised[e3aName(s, id)] = v
		}
		require.Equal(t, map[string]int{"Spaceship Command": 3, "Gallente Frigate": 3, "Gallente Destroyer": 3, "Gunnery": 2}, raised)
		di := s.ResolveNames([]string{"Drone Interfacing"})["Drone Interfacing"]
		require.Zero(t, p.Skills[strconv.Itoa(di)], "no Drone Interfacing")
		drones := s.ResolveNames([]string{"Drones"})["Drones"]
		require.Equal(t, 4, p.Skills[strconv.Itoa(drones)])
		require.Equal(t, []string{
			"10MN Afterburner II", "Damage Control II", "Drone Link Augmentor II", "Drone Navigation Computer II",
			"Hammerhead II", "Hobgoblin II", "Medium Armor Repairer II", "Medium Capacitor Booster II",
			"Multispectrum Energized Membrane II", "Valkyrie II", "Warp Scrambler II",
		}, e3aUnusable(t, s, p, 2), "the 11 unusable lines of reference 2")
	})
	t.Run("missiler", func(t *testing.T) {
		fliesAll("missiler")
		p := pf.Profiles["missiler"]
		require.Len(t, p.Dropped, 1)
		require.Equal(t, "Bomb Deployment", e3aName(s, p.Dropped[0]))
		hml := s.ResolveNames([]string{"Heavy Missile Launcher II"})["Heavy Missile Launcher II"]
		r := e3aCanUse(s, p, t, hml)
		require.False(t, r.OK)
		require.Equal(t, map[string]int{"Heavy Missile Specialization": 1}, e3aMissingNames(s, r))
		// Cerberus needs Caldari Cruiser V and HAC I; HAC's own prerequisites are met.
		require.Empty(t, e3aCanUse(s, p, t, e3aHullID(t, "Cerberus")).Missing)
	})
	t.Run("tanker", func(t *testing.T) {
		fliesOnly("tanker", "Vexor", "Thorax", "Myrmidon", "Brutix")
		raised := map[string]int{}
		for k, v := range pf.Profiles["tanker"].Raised {
			id, _ := strconv.Atoi(k)
			raised[e3aName(s, id)] = v
		}
		require.Equal(t, map[string]int{
			"Spaceship Command": 3, "Gallente Frigate": 3, "Gallente Destroyer": 3,
			"Gallente Cruiser": 3, "Power Grid Management": 3,
		}, raised)
	})

	// worked-pair table
	missing := func(profile, hull string) map[string]int {
		return e3aMissingNames(s, e3aCanUse(s, pf.Profiles[profile], t, e3aHullID(t, hull)))
	}
	t.Run("pairs", func(t *testing.T) {
		m := missing("all0", "Rifter")
		require.Equal(t, 1, m["Spaceship Command"])
		require.Equal(t, 1, m["Minmatar Frigate"])
		require.False(t, flies("all0", "Rifter"))
		for _, p := range []string{"allI", "allIII", "alpha"} {
			require.True(t, flies(p, "Rifter"), "Rifter x %s", p)
		}
		require.False(t, flies("all0", "Vexor"))
		require.True(t, flies("droner", "Vexor"))
		require.True(t, flies("alpha", "Vexor"))
		require.True(t, flies("missiler", "Cerberus"))
		require.False(t, flies("fitIII", "Cerberus"))
		require.Equal(t, 1, missing("fitIII", "Cerberus")["Heavy Assault Cruisers"])
		require.False(t, flies("fitIII", "Tengu"))
		require.Equal(t, 1, missing("fitIII", "Tengu")["Caldari Core Systems"])
		for _, p := range []string{"allIII", "alpha", "allI"} {
			require.True(t, flies(p, "Megathron"), "Megathron x %s", p)
		}
		require.True(t, flies("tanker", "Myrmidon"))
		require.True(t, flies("droner", "Myrmidon"))
		require.True(t, flies("allI", "Cerberus"))
	})
}
