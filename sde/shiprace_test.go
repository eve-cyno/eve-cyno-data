package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The curated SDE has no invTypes.raceID, so the race of a hull is read from its
// racial ship-handling skill (2026-10 SDE). chrRaces ids: Caldari 1, Minmatar 2,
// Amarr 4, Gallente 8.
func TestShipRaceIDs_realSDE(t *testing.T) {
	s := testSDE(t)
	defer s.Close()
	races := s.ShipRaceIDs()

	want := map[string][]int{
		"Ishtar":            {8}, // Gallente Cruiser + Heavy Assault Cruisers
		"Dominix":           {8},
		"Myrmidon":          {8},
		"Maulus Navy Issue": {8},
		"Dragoon":           {4}, // Amarr Destroyer
		"Armageddon":        {4},
		"Curse":             {4}, // Amarr Cruiser + Recon Ships
		"Rifter":            {2},
		"Caracal":           {1},
		"Gila":              {1, 8}, // Caldari Cruiser + Gallente Cruiser
	}
	for hull, ids := range want {
		require.Equal(t, ids, races[hull], hull)
	}

	// Hulls flown on no racial skill carry no race.
	for _, hull := range []string{"Orca", "Praxis", "Gnosis"} {
		require.Empty(t, races[hull], hull)
	}
	// Skills and other non-hull types never appear.
	require.NotContains(t, races, "Gallente Cruiser")
	require.NotContains(t, races, "Warp Scrambler II")
}

// Launcher hardpoints are dogma attribute 101 (launcherSlotsLeft) on the hull type.
func TestShipLauncherHardpoints_realSDE(t *testing.T) {
	s := testSDE(t)
	defer s.Close()
	launchers := s.ShipLauncherHardpoints()

	want := map[string]int{
		"Armageddon": 5,
		"Prophecy":   4,
		"Dragoon":    3,
		"Arbitrator": 3,
		"Caracal":    5,
		"Rifter":     2,
	}
	for hull, n := range want {
		require.Equal(t, n, launchers[hull], hull)
	}

	// Hulls with no launcher hardpoint are absent: the pure turret / drone hulls.
	for _, hull := range []string{"Ishtar", "Dominix", "Myrmidon", "Punisher"} {
		require.NotContains(t, launchers, hull)
	}
	// Modules, skills and other non-hull types never appear.
	require.NotContains(t, launchers, "Heavy Missile Launcher II")
	require.NotContains(t, launchers, "Gallente Cruiser")
}
