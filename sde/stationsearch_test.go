package sde

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFindStations_NearestL4SecurityAgentsFromDodixie(t *testing.T) {
	s := testSDE(t)
	got := s.FindStations(StationFilter{
		Near:  &NearFilter{Origin: sysID(t, s, "Dodixie"), MaxJumps: 5},
		Agent: &AgentFilter{Level: 4, DivisionID: 24, TypeIDs: []int{2}},
	})
	require.Len(t, got, 25)
	require.Equal(t, "Dodixie 9 - Moon 20 - Federation Navy Assembly Plant", got[0].Name)
	require.Equal(t, 0, got[0].Jumps)
	require.Equal(t, "Saonellieu Baerse", got[0].Agents[0].Name)
	require.True(t, got[0].Agents[0].Locator)
	require.Equal(t, []string{"Auvergne", "Aunia"}, []string{got[1].System, got[2].System})
	require.Equal(t, []int{1, 2}, []int{got[1].Jumps, got[2].Jumps})
}

func TestFindStations_ServicesNearDodixie(t *testing.T) {
	s := testSDE(t)
	got := s.FindStations(StationFilter{Near: &NearFilter{Origin: sysID(t, s, "Dodixie"), MaxJumps: 1}, Services: []string{"Repair Facilities"}})
	require.Len(t, got, 10)
	require.Equal(t, "Dodixie 5 - Paragon Fulfillment Center", got[0].Name)
	require.Equal(t, "Dodixie 9 - Moon 20 - Federation Navy Assembly Plant", got[1].Name)
}

// TestFindStations_TablesLoadOncePerGeneration pins SV-M10: the corporation, service and
// agent tables are read once per SDE generation, not once per request.
func TestFindStations_TablesLoadOncePerGeneration(t *testing.T) {
	s := testSDE(t)
	f := StationFilter{
		Near:  &NearFilter{Origin: sysID(t, s, "Dodixie"), MaxJumps: 5},
		Agent: &AgentFilter{Level: 4, DivisionID: 24, TypeIDs: []int{2}},
	}
	first := s.FindStations(f)
	require.Len(t, first, 25)
	loads := s.stationLoads.Load()
	require.Equal(t, int64(1), loads)
	second := s.FindStations(f)
	other := s.FindStations(StationFilter{Services: []string{"Reprocessing Plant"}, SystemID: sysID(t, s, "Dodixie")})
	require.NotEmpty(t, other)
	require.Equal(t, loads, s.stationLoads.Load(), "later calls must not reload")
	require.Equal(t, first, second)
}

// TestFindStations_ReturnedSlicesAreNotShared pins that a caller changing the services or
// agents of one answer leaves the next answer, and the cache, unchanged.
func TestFindStations_ReturnedSlicesAreNotShared(t *testing.T) {
	s := testSDE(t)
	f := StationFilter{
		Near:  &NearFilter{Origin: sysID(t, s, "Dodixie"), MaxJumps: 2},
		Agent: &AgentFilter{Level: 4, DivisionID: 24, TypeIDs: []int{2}},
	}
	want := s.FindStations(f)
	require.NotEmpty(t, want)
	got := s.FindStations(f)
	for i := range got {
		for j := range got[i].Services {
			got[i].Services[j] = "mutated"
		}
		for j := range got[i].Agents {
			got[i].Agents[j].Name = "mutated"
		}
	}
	require.Equal(t, want, s.FindStations(f))
}

// TestStationTables_ReloadOnNewGeneration pins that a replaced SDE file drops the cache.
func TestStationTables_ReloadOnNewGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeSDE(t, path, []string{"build_old"}, nil)
	s, clk := openWithClock(t, path, time.Minute)
	s.stationTables()
	s.stationTables()
	require.Equal(t, int64(1), s.stationLoads.Load())
	writeSDE(t, path, []string{"build_new"}, nil)
	clk.advance(2 * time.Minute)
	s.stationTables()
	s.stationTables()
	require.Equal(t, int64(2), s.stationLoads.Load())
}
