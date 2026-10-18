package sde

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func facts(t *testing.T, s *SDE, name string) SystemFacts {
	t.Helper()
	f, ok := s.SystemFactsByID(sysID(t, s, name))
	require.True(t, ok, name)
	return f
}

func TestSystemFacts_Jita(t *testing.T) {
	s := routeSDE(t)
	f := facts(t, s, "Jita")
	require.Equal(t, "Jita", f.Name)
	require.Equal(t, "The Forge", f.Region)
	require.Equal(t, "Kimotoro", f.Constellation)
	require.Equal(t, 0, f.Belts)
	require.Equal(t, 8, f.Planets)
	require.Equal(t, 33, f.Moons)
	require.Equal(t, 18, f.NPCStations)
	require.Equal(t, 7, f.WormholeClass)
	require.Equal(t, 7, f.Stargates)
	require.Equal(t, f.Stargates, len(f.Neighbours))
	sum := 0
	for _, n := range f.PlanetsByType {
		sum += n
	}
	require.Equal(t, f.Planets, sum)
	require.Contains(t, f.StationServices, "Repair Facilities")
	require.Greater(t, f.FactionID, 0)
	require.NotEmpty(t, f.Faction)
}

func TestSystemFacts_WormholeClassAndEffect(t *testing.T) {
	s := routeSDE(t)
	j := facts(t, s, "J100009")
	require.Equal(t, 3, j.WormholeClass)
	require.Equal(t, "Pulsar", j.WormholeEffect)
	th := facts(t, s, "Thera")
	require.Equal(t, 12, th.WormholeClass)
	require.Equal(t, "", th.WormholeEffect)
	require.Equal(t, 14, th.Planets)
	require.Equal(t, 4, th.NPCStations)
	require.Equal(t, 9, facts(t, s, "C-N4OD").WormholeClass)
	require.Equal(t, 25, facts(t, s, "Ala").WormholeClass)
	require.Equal(t, 4, facts(t, s, "Tama").Belts)
	require.Equal(t, 8, facts(t, s, "Tama").WormholeClass)
	_, ok := s.SystemFactsByID(-1)
	require.False(t, ok)
}

func TestFindSystems_SecurityRegionSort(t *testing.T) {
	s := routeSDE(t)
	forge := s.ResolveRegionName("The Forge")
	require.NotNil(t, forge)
	min := 0.5
	rows := s.FindSystems(SystemFilter{RegionID: *forge, SecMin: &min, Sort: "security"})
	require.NotEmpty(t, rows)
	for i, r := range rows {
		require.Equal(t, "The Forge", r.Region)
		require.GreaterOrEqual(t, DisplaySecurity(r.Security), 0.5)
		require.Equal(t, -1, r.Jumps)
		if i > 0 {
			require.LessOrEqual(t, r.Security, rows[i-1].Security)
		}
	}
}

func TestFindSystems_NearRanksByJumps(t *testing.T) {
	s := routeSDE(t)
	jita := sysID(t, s, "Jita")
	rows := s.FindSystems(SystemFilter{Near: &NearFilter{Origin: jita, MaxJumps: 1}})
	require.Len(t, rows, 8)
	require.Equal(t, "Jita", rows[0].Name)
	require.Equal(t, 0, rows[0].Jumps)
	for i := 1; i < len(rows); i++ {
		require.Equal(t, 1, rows[i].Jumps)
		if i > 1 {
			require.Less(t, rows[i-1].Name, rows[i].Name)
		}
	}
	pref := s.FindSystems(SystemFilter{Near: &NearFilter{Origin: jita, MaxJumps: 1, PreferHighSec: true}})
	require.Len(t, pref, 8)
	require.True(t, pref[1].HighsecRoute)
}

func TestDisplaySecurity(t *testing.T) {
	require.Equal(t, 0.4, DisplaySecurity(0.449))
	require.Equal(t, 0.5, DisplaySecurity(0.45))
	require.Equal(t, 0.1, DisplaySecurity(0.04))
	require.Equal(t, -0.3, DisplaySecurity(-0.3))
}

func TestFindSystems_SecMinUsesDisplayedSecurity(t *testing.T) {
	s := routeSDE(t)
	forge := s.ResolveRegionName("The Forge")
	require.NotNil(t, forge)
	ot := facts(t, s, "Otitoh")
	require.Less(t, ot.Security, 0.5)
	min := 0.5
	var found bool
	for _, r := range s.FindSystems(SystemFilter{RegionID: *forge, SecMin: &min}) {
		found = found || r.Name == "Otitoh"
	}
	require.True(t, found, "Otitoh (raw %v) shows as 0.5", ot.Security)
}

func TestFindSystems_NearPreferHighSecLowsecTarget(t *testing.T) {
	s := routeSDE(t)
	jita := sysID(t, s, "Jita")
	tama := facts(t, s, "Tama")
	require.Less(t, tama.Security, 0.45)
	for _, m := range s.FindSystems(SystemFilter{OnlyIDs: map[int]bool{tama.ID: true}, Near: &NearFilter{Origin: jita, PreferHighSec: true}}) {
		require.False(t, m.HighsecRoute)
		require.Equal(t, s.JumpDistances(jita, false, nil)[tama.ID], m.Jumps)
		return
	}
	t.Fatal("Tama not reachable from Jita")
}

func TestFindSystems_MaxJumpsZeroIsUnbounded(t *testing.T) {
	s := routeSDE(t)
	jita := sysID(t, s, "Jita")
	far := 0
	for id, d := range s.JumpDistances(jita, false, nil) {
		if d > 10 {
			far = id
			break
		}
	}
	require.NotZero(t, far)
	rows := s.FindSystems(SystemFilter{OnlyIDs: map[int]bool{far: true}, Near: &NearFilter{Origin: jita}})
	require.Len(t, rows, 1)
	require.Greater(t, rows[0].Jumps, 10)
}

func TestFindSystems_UnknownSortFallsBackToName(t *testing.T) {
	s := routeSDE(t)
	rows := s.FindSystems(SystemFilter{RegionID: *s.ResolveRegionName("The Forge"), Sort: "bogus"})
	require.NotEmpty(t, rows)
	for i := 1; i < len(rows); i++ {
		require.LessOrEqual(t, rows[i-1].Name, rows[i].Name)
	}
}

func TestFindSystems_NearLeavesOutUnreachable(t *testing.T) {
	s := routeSDE(t)
	rows := s.FindSystems(SystemFilter{WormholeClass: 12, Near: &NearFilter{Origin: sysID(t, s, "Jita")}})
	require.Empty(t, rows)
}

func TestFindSystems_ServicesPlanetsBeltsAndOnlyIDs(t *testing.T) {
	s := routeSDE(t)
	yes := true
	rows := s.FindSystems(SystemFilter{
		HasNPCStation:   &yes,
		StationServices: []string{"Reprocessing Plant"},
		PlanetTypes:     []string{"Gas", "Ice"},
		MinBelts:        2,
	})
	require.NotEmpty(t, rows)
	for _, r := range rows {
		require.Positive(t, r.NPCStations)
		require.Contains(t, r.StationServices, "Reprocessing Plant")
		require.Positive(t, r.PlanetsByType["Gas"])
		require.Positive(t, r.PlanetsByType["Ice"])
		require.GreaterOrEqual(t, r.Belts, 2)
	}
	only := map[int]bool{rows[0].ID: true}
	got := s.FindSystems(SystemFilter{OnlyIDs: only})
	require.Len(t, got, 1)
	require.Equal(t, rows[0].ID, got[0].ID)
	require.Empty(t, s.FindSystems(SystemFilter{OnlyIDs: map[int]bool{}}))
}

func TestVocabularies(t *testing.T) {
	s := routeSDE(t)
	require.Subset(t, s.PlanetTypeNames(), []string{"Ice", "Gas", "Shattered"})
	require.Contains(t, s.StationServiceNames(), "Reprocessing Plant")
	require.NotNil(t, s.ResolveConstellationName("kimotoro"))
	require.Nil(t, s.ResolveConstellationName("nowhere"))
	require.NotNil(t, s.ResolveFactionName("caldari state"))
}

// The system index holds the systems players can enter. Left out: the 400 Abyssal and Void
// deadspace pseudo-systems (AD001.., V-001..), the gateless Jove regions UUA-F4, J7HZ-F and A821-A
// (no gate route from Jita; includes the wormhole class 10/11 pocket) and the global PLEX market
// pseudo-region GPMR-01 (GPMS-01). Pochven stays (filament entry).
func TestSystemIndex_LeavesOutDeadspacePseudoSystemsAndUnreachablePockets(t *testing.T) {
	s := routeSDE(t)
	for _, name := range []string{"AD001", "AD200", "V-001", "V-200", "VM-QFU", "Q-KCK3", "09-8TH", "GPMS-01", "PZP1-D", "R1KE-A", "2-NF2Z"} {
		id := s.ResolveSystemName(name)
		require.NotNil(t, id, name+" stays in the raw name table")
		_, ok := s.SystemFactsByID(*id)
		require.False(t, ok, name)
	}
	for _, name := range []string{"Jita", "Thera", "J100009", "Kuharah"} {
		_, ok := s.SystemFactsByID(sysID(t, s, name))
		require.True(t, ok, name)
	}
	require.Empty(t, s.FindSystems(SystemFilter{WormholeClass: 10}))
	require.Empty(t, s.FindSystems(SystemFilter{WormholeClass: 11}))
	for _, m := range s.FindSystems(SystemFilter{SecMax: &minusPointNine}) {
		require.NotRegexp(t, `^(AD|V-)\d{3}$`, m.Name)
	}
}

var minusPointNine = -0.9

func TestDisplaySecurity_NoNegativeZero(t *testing.T) {
	// KFIE-Z has a true security of about -0.0035: the game shows +0.0, never -0.0.
	require.Equal(t, "+0.0", fmt.Sprintf("%+.1f", DisplaySecurity(-0.0035)))
	require.Equal(t, -0.1, DisplaySecurity(-0.05))
	require.Equal(t, 0.1, DisplaySecurity(0.04))
}

func TestFindSystems_JumpRange(t *testing.T) {
	s := routeSDE(t)
	lo, hi, yes := 0.1, 0.4, true
	origin := sysID(t, s, "EC-P8R")
	got := s.FindSystems(SystemFilter{
		SecMin: &lo, SecMax: &hi, HasNPCStation: &yes,
		JumpRange: &JumpRangeFilter{Origin: origin, Ship: jumpShip(t, s, "Rorqual"), RangeLY: 10},
	})
	require.Len(t, got, 49)
	for i, want := range []struct {
		name string
		ly   float64
	}{{"Anin", 1.162}, {"Nannaras", 1.267}, {"Iralaja", 1.797}} {
		require.Equal(t, want.name, got[i].Name)
		require.InDelta(t, want.ly, got[i].LY, 0.0005)
	}
	for _, m := range got {
		require.LessOrEqual(t, m.LY, 10.0)
		require.NotEqual(t, origin, m.ID)
		require.Equal(t, -1, m.Jumps)
	}

	av := s.FindSystems(SystemFilter{JumpRange: &JumpRangeFilter{Origin: sysID(t, s, "1DQ1-A"), Ship: jumpShip(t, s, "Avatar"), RangeLY: 6}})
	require.Len(t, av, 76)
	require.Equal(t, "8WA-Z6", av[0].Name)

	// Without a jump range LY is -1.
	for _, m := range s.FindSystems(SystemFilter{ConstellationID: got[0].ConstellationID}) {
		require.Equal(t, -1.0, m.LY)
	}
}
