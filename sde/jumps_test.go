package sde

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde/sdetest"
)

func jumpShip(t *testing.T, s *SDE, name string) JumpShip {
	t.Helper()
	sh, ok := s.ResolveJumpShip(name)
	require.True(t, ok, name)
	return sh
}

func stepNames(t *testing.T, s *SDE, steps []JumpStep) []string {
	t.Helper()
	out := make([]string, len(steps))
	for i, st := range steps {
		out[i] = *s.GetSystemName(st.To)
	}
	return out
}

func sumLY(steps []JumpStep) float64 {
	t := 0.0
	for _, st := range steps {
		t += st.LY
	}
	return t
}

var all5 = JumpSkills{Calibration: 5, FuelConservation: 5, JumpFreighters: 5}

func TestJumpShips_FromTheSDE(t *testing.T) {
	s := routeSDE(t)
	ships := s.JumpShips()
	require.Len(t, ships, 57)
	groups := map[string]int{}
	for _, sh := range ships {
		groups[sh.Group]++
	}
	require.Equal(t, map[string]int{
		"Black Ops": 6, "Capital Industrial Ship": 1, "Carrier": 4, "Command Carrier": 4, "Dreadnought": 13,
		"Force Auxiliary": 6, "Jump Freighter": 4, "Lancer Dreadnought": 4, "Supercarrier": 6, "Titan": 8,
		"Upwell Jump Bridge": 1,
	}, groups)

	rhea := jumpShip(t, s, "Rhea")
	require.Equal(t, "Jump Freighter", rhea.Group)
	require.Equal(t, 5.0, rhea.BaseRangeLY)
	require.Equal(t, "Nitrogen Isotopes", rhea.FuelName)
	require.Equal(t, 0.03, rhea.FuelUnitVolume)
	require.Equal(t, 10000.0, rhea.BaseFuelPerLY)
	require.Equal(t, -10.0, rhea.JumpFreighterBonus)
	require.False(t, rhea.NoHighsec)

	archon := jumpShip(t, s, "Archon")
	require.True(t, archon.NoHighsec)
	require.True(t, archon.CanConduit)
	require.False(t, archon.CanBridge)
	avatar := jumpShip(t, s, "Avatar")
	require.True(t, avatar.CanBridge)
	require.False(t, avatar.CanConduit)
	for _, n := range []string{"Panther", "Rorqual"} {
		sh := jumpShip(t, s, n)
		require.True(t, sh.CanBridge && sh.CanConduit, n)
	}
	require.True(t, jumpShip(t, s, "Rorqual").NoHighsec)
	require.False(t, jumpShip(t, s, "Panther").NoHighsec)

	for _, n := range []string{" jump bridge ", "ANSIBLEX"} {
		sh := jumpShip(t, s, n)
		require.Equal(t, 35841, sh.TypeID)
		require.True(t, sh.Ansiblex)
		require.Equal(t, 5.0, sh.BaseRangeLY)
	}
	_, ok := s.ResolveJumpShip("Raven")
	require.False(t, ok)
}

func TestJumpRangeAndFuel(t *testing.T) {
	s := routeSDE(t)
	rng := func(n string, sk JumpSkills) float64 { return s.JumpRangeLY(jumpShip(t, s, n), sk) }
	fuel := func(n string, sk JumpSkills) float64 { return s.JumpFuelPerLY(jumpShip(t, s, n), sk) }
	require.InDelta(t, 10.0, rng("Rhea", all5), 1e-9)
	require.InDelta(t, 2500.0, fuel("Rhea", all5), 1e-6)
	require.InDelta(t, 7.0, rng("Archon", all5), 1e-9)
	require.InDelta(t, 1500.0, fuel("Archon", all5), 1e-6)
	require.InDelta(t, 8.0, rng("Panther", all5), 1e-9)
	require.InDelta(t, 350.0, fuel("Panther", all5), 1e-6)
	require.InDelta(t, 6.0, rng("Avatar", all5), 1e-9)
	require.InDelta(t, 7.5, rng("Salvation", all5), 1e-9)
	require.InDelta(t, 8.0, rng("Hubris", all5), 1e-9)
	require.InDelta(t, 9.0, rng("Rhea", JumpSkills{4, 5, 5}), 1e-9)
	require.InDelta(t, 3600.0, fuel("Rhea", JumpSkills{5, 4, 4}), 1e-6)
	require.InDelta(t, 5.0, rng("Rhea", JumpSkills{}), 1e-9)
	require.InDelta(t, 10000.0, fuel("Rhea", JumpSkills{}), 1e-6)
	for _, sk := range []JumpSkills{{}, all5, {2, 3, 1}} {
		require.Equal(t, 5.0, rng("Ansiblex", sk))
		require.Equal(t, 0.0, fuel("Ansiblex", sk))
	}
}

func TestDistanceLY(t *testing.T) {
	s := routeSDE(t)
	id := func(n string) int { return sysID(t, s, n) }
	for _, c := range []struct {
		a, b string
		want float64
	}{{"Amamake", "Sahda", 9.230}, {"Enal", "Amafi", 9.883}, {"KFIE-Z", "1DQ1-A", 6.978}, {"Amamake", "1DQ1-A", 43.126}} {
		d, ok := s.DistanceLY(id(c.a), id(c.b))
		require.True(t, ok)
		require.InDelta(t, c.want, d, 0.0005, c.a+"-"+c.b)
	}
	_, ok := s.DistanceLY(id("Jita"), 1)
	require.False(t, ok)
}

func TestJumpPlaces(t *testing.T) {
	s := routeSDE(t)
	rhea, archon := jumpShip(t, s, "Rhea"), jumpShip(t, s, "Archon")
	panther, rorqual, ansi := jumpShip(t, s, "Panther"), jumpShip(t, s, "Rorqual"), jumpShip(t, s, "Ansiblex")
	id := func(n string) int { return sysID(t, s, n) }
	// A821-A is a gateless Jove region: its systems are not in the system index (no one can enter
	// them), but the jump rules still answer for the raw name.
	a821 := "2-NF2Z"

	for name, want := range map[string]JumpPlace{
		"Jita": JumpHighsec, "Thera": JumpWormhole, "J100009": JumpWormhole, "Niarja": JumpPochven,
		"Zarzakh": JumpZarzakh, a821: JumpOffNetwork, "Amamake": JumpOK, "1DQ1-A": JumpOK,
	} {
		require.Equal(t, want, s.JumpTarget(id(name), rhea), name)
	}
	require.Equal(t, JumpOK, s.JumpOrigin(id("Jita"), rhea))
	require.Equal(t, JumpOK, s.JumpOrigin(id("Jita"), panther))
	require.Equal(t, JumpHighsec, s.JumpOrigin(id("Jita"), archon))
	require.Equal(t, JumpHighsec, s.JumpOrigin(id("Jita"), rorqual))
	require.Equal(t, JumpOK, s.JumpOrigin(id("Niarja"), rhea))
	require.Equal(t, JumpOK, s.JumpOrigin(id("Zarzakh"), rhea))
	require.Equal(t, JumpWormhole, s.JumpOrigin(id("Thera"), rhea))

	syndicate := s.FindSystems(SystemFilter{RegionID: *s.ResolveRegionName("Syndicate")})
	require.NotEmpty(t, syndicate)
	require.Equal(t, JumpNoSovereignty, s.JumpTarget(id("Amamake"), ansi))
	require.Equal(t, JumpNoSovereignty, s.JumpTarget(syndicate[0].ID, ansi))
	require.Equal(t, JumpOK, s.JumpTarget(id("1DQ1-A"), ansi))
	require.Equal(t, JumpNoSovereignty, s.JumpOrigin(id("Zarzakh"), ansi))
}

func TestJumpTargetCounts(t *testing.T) {
	s := routeSDE(t)
	idx := s.ensureJumpIndex()
	// Pinned on the SDE build of 2026-10-12; on a new SDE update only after checking why they moved.
	require.Len(t, idx.network, 5228)
	require.Len(t, idx.cyno, 3981)
	require.Len(t, idx.ansiblex, 2712)
}

func TestPlanJumpRoute_RheaAmamakeTo1DQ1A(t *testing.T) {
	s := routeSDE(t)
	rhea := jumpShip(t, s, "Rhea")
	steps, _, ok := s.PlanJumpRoute(sysID(t, s, "Amamake"), []int{sysID(t, s, "1DQ1-A")}, rhea, JumpRouteOptions{RangeLY: 10})
	require.True(t, ok)
	require.Equal(t, []string{"Ayeroilen", "Ami", "Tegheon", "T8T-RA", "1DQ1-A"}, stepNames(t, s, steps))
	require.InDelta(t, 44.416, sumLY(steps), 0.0005)
}

func TestPlanJumpRoute_ViaSahdaMatchesPublishedPlanner(t *testing.T) {
	s := routeSDE(t)
	rhea := jumpShip(t, s, "Rhea")
	// The figures a public jump planner shows for this trip (Rhea, all skills 5): 5 hops, 45.384 ly.
	steps, _, ok := s.PlanJumpRoute(sysID(t, s, "Amamake"), []int{sysID(t, s, "Sahda"), sysID(t, s, "1DQ1-A")}, rhea, JumpRouteOptions{RangeLY: 10})
	require.True(t, ok)
	require.Equal(t, []string{"Sahda", "Enal", "Amafi", "KFIE-Z", "1DQ1-A"}, stepNames(t, s, steps))
	for i, want := range []float64{9.230, 9.407, 9.883, 9.885, 6.978} {
		require.InDelta(t, want, steps[i].LY, 0.0005, i)
	}
	require.InDelta(t, 45.384, sumLY(steps), 0.0005)
}

func TestPlanJumpRoute_PreferStations(t *testing.T) {
	s := routeSDE(t)
	steps, _, ok := s.PlanJumpRoute(sysID(t, s, "Amamake"), []int{sysID(t, s, "1DQ1-A")}, jumpShip(t, s, "Rhea"), JumpRouteOptions{RangeLY: 10, PreferStations: true})
	require.True(t, ok)
	names := stepNames(t, s, steps)
	require.Len(t, names, 5)
	require.Equal(t, "Chamemi", names[2])
	require.InDelta(t, 44.741, sumLY(steps), 0.0005)
}

func TestPlanJumpRoute_AvoidSystemsAndRegions(t *testing.T) {
	s := routeSDE(t)
	rhea := jumpShip(t, s, "Rhea")
	from, to := sysID(t, s, "Amamake"), sysID(t, s, "1DQ1-A")

	steps, _, ok := s.PlanJumpRoute(from, []int{to}, rhea, JumpRouteOptions{RangeLY: 10, Avoid: map[int]bool{sysID(t, s, "Tegheon"): true}})
	require.True(t, ok)
	require.Equal(t, []string{"Ayeroilen", "Ami", "Rilera", "T8T-RA", "1DQ1-A"}, stepNames(t, s, steps))
	require.InDelta(t, 44.656, sumLY(steps), 0.0005)

	steps, _, ok = s.PlanJumpRoute(from, []int{to}, rhea, JumpRouteOptions{RangeLY: 10, AvoidRegions: map[int]bool{*s.ResolveRegionName("Khanid"): true}})
	require.True(t, ok)
	require.Equal(t, []string{"Sahda", "Menai", "Perbhe", "T8T-RA", "1DQ1-A"}, stepNames(t, s, steps))
	require.InDelta(t, 47.154, sumLY(steps), 0.0005)

	steps, _, ok = s.PlanJumpRoute(from, []int{to}, rhea, JumpRouteOptions{RangeLY: 10, Avoid: map[int]bool{to: true}})
	require.True(t, ok, "the stops are exempt from avoid")
	require.Equal(t, "1DQ1-A", stepNames(t, s, steps)[len(steps)-1])
}

func TestPlanJumpRoute_RangeDrivesJumpCount(t *testing.T) {
	s := routeSDE(t)
	from, to := sysID(t, s, "Amamake"), sysID(t, s, "1DQ1-A")
	for _, c := range []struct {
		ship  string
		sk    JumpSkills
		steps int
		ly    float64
	}{
		{"Rhea", JumpSkills{4, 5, 5}, 6, 44.899},
		{"Archon", all5, 8, 45.975},
		{"Avatar", all5, 10, 51.358},
		{"Avatar", JumpSkills{}, 34, 82.320},
	} {
		sh := jumpShip(t, s, c.ship)
		steps, _, ok := s.PlanJumpRoute(from, []int{to}, sh, JumpRouteOptions{RangeLY: s.JumpRangeLY(sh, c.sk)})
		require.True(t, ok, c.ship)
		require.Len(t, steps, c.steps, c.ship)
		require.InDelta(t, c.ly, sumLY(steps), 0.0005, c.ship)
	}
}

func TestPlanJumpRoute_FromHighsecAndFromPochven(t *testing.T) {
	s := routeSDE(t)
	rhea := jumpShip(t, s, "Rhea")
	to := sysID(t, s, "1DQ1-A")
	steps, _, ok := s.PlanJumpRoute(sysID(t, s, "Jita"), []int{to}, rhea, JumpRouteOptions{RangeLY: 10})
	require.True(t, ok)
	require.Equal(t, []string{"Olettiers", "Ahbazon", "Ranni", "Nahrneder", "6Q-R50", "1DQ1-A"}, stepNames(t, s, steps))
	require.InDelta(t, 52.222, sumLY(steps), 0.0005)

	steps, _, ok = s.PlanJumpRoute(sysID(t, s, "Niarja"), []int{sysID(t, s, "Amamake")}, rhea, JumpRouteOptions{RangeLY: 10})
	require.True(t, ok)
	require.Len(t, steps, 1)
	require.InDelta(t, 7.167, steps[0].LY, 0.0005)
}

func TestPlanJumpRoute_Ansiblex(t *testing.T) {
	s := routeSDE(t)
	ansi := jumpShip(t, s, "Ansiblex")
	from := sysID(t, s, "1DQ1-A")
	steps, _, ok := s.PlanJumpRoute(from, []int{sysID(t, s, "MVUO-F")}, ansi, JumpRouteOptions{RangeLY: 5})
	require.True(t, ok)
	require.Len(t, steps, 6)
	require.InDelta(t, 23.067, sumLY(steps), 0.0005)
	for _, st := range steps {
		f, _ := s.SystemFactsByID(st.To)
		require.Equal(t, 0, f.FactionID, f.Name)
		require.LessOrEqual(t, DisplaySecurity(f.Security), 0.0, f.Name)
	}
	_, leg, ok := s.PlanJumpRoute(from, []int{sysID(t, s, "C-N4OD")}, ansi, JumpRouteOptions{RangeLY: 5})
	require.False(t, ok)
	require.Equal(t, 0, leg)
}

func TestSystemsInJumpRange(t *testing.T) {
	s := routeSDE(t)
	nearest := func(m map[int]float64) (string, float64) {
		best, bd := 0, 1e18
		for id, d := range m {
			if d < bd || (d == bd && id < best) {
				best, bd = id, d
			}
		}
		return *s.GetSystemName(best), bd
	}
	for _, c := range []struct {
		ship, origin, near string
		rng, nearLY        float64
		n                  int
	}{
		{"Avatar", "1DQ1-A", "8WA-Z6", 6, 0.508, 76},
		{"Rhea", "Amamake", "Auga", 10, 0.432, 152},
		{"Panther", "Jita", "Eruka", 8, 4.532, 62},
		{"Ansiblex", "1DQ1-A", "", 5, 0, 56},
	} {
		origin := sysID(t, s, c.origin)
		m := s.SystemsInJumpRange(origin, jumpShip(t, s, c.ship), c.rng)
		require.Len(t, m, c.n, c.ship)
		require.NotContains(t, m, origin)
		if c.near != "" {
			name, d := nearest(m)
			require.Equal(t, c.near, name)
			require.InDelta(t, c.nearLY, d, 0.0005)
		}
	}
}

func TestJumpIndex_BuiltOnceUnderConcurrentUse(t *testing.T) {
	s := routeSDE(t)
	var wg sync.WaitGroup
	got := make([]*jumpIndex, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = s.ensureJumpIndex()
		}()
	}
	wg.Wait()
	for _, g := range got {
		require.Same(t, got[0], g, "one index per SDE generation")
	}
}

func BenchmarkPlanJumpRoute(b *testing.B) {
	s, err := Open(sdetest.Path(b))
	require.NoError(b, err)
	b.Cleanup(func() { _ = s.Close() })
	sh, ok := s.ResolveJumpShip("Rhea")
	require.True(b, ok)
	from, to := *s.ResolveSystemName("Amamake"), *s.ResolveSystemName("1DQ1-A")
	_, _, _ = s.PlanJumpRoute(from, []int{to}, sh, JumpRouteOptions{RangeLY: 10}) // warm the index
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, ok := s.PlanJumpRoute(from, []int{to}, sh, JumpRouteOptions{RangeLY: 10}); !ok {
			b.Fatal("no route")
		}
	}
}

func TestPlanJumpRoute_NoStopsIsAnEmptyRoute(t *testing.T) {
	s := routeSDE(t)
	steps, _, ok := s.PlanJumpRoute(sysID(t, s, "Amamake"), nil, jumpShip(t, s, "Rhea"), JumpRouteOptions{RangeLY: 10})
	require.True(t, ok)
	require.Empty(t, steps)
}

func TestJumpIndex_RebuiltOnHotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	first := s.ensureJumpIndex()
	require.Equal(t, uint64(1), first.gen)
	require.Same(t, first, s.ensureJumpIndex(), "same generation, same index")

	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	second := s.ensureJumpIndex()
	require.Equal(t, uint64(2), second.gen)
	require.NotSame(t, first, second, "a new SDE generation rebuilds the index")
	require.Same(t, second, s.ensureJumpIndex())
}
