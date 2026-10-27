package sde

import (
	"testing"

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
