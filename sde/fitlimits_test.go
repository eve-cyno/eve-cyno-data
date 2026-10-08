package sde

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/sde/sdetest"
)

func testSDE(t *testing.T) *SDE {
	t.Helper()
	s, err := Open(sdetest.Path(t))
	if err != nil || !s.Available() {
		sdetest.Skip(t, "SDE sqlite not available; skipping")
	}
	return s
}

func TestGetShipSlotLimits_Rifter(t *testing.T) {
	s := testSDE(t)
	// Rifter typeID = 587 (frigate: 3 hi / 3 mid / 4 low / 3 rig per this SDE dump).
	lim := s.GetShipSlotLimits(587)
	require.Equal(t, 3, lim[14], "hi")
	require.Equal(t, 3, lim[13], "mid")
	require.Equal(t, 4, lim[12], "low")
	require.Equal(t, 3, lim[1137], "rig")
}

func TestIsShip(t *testing.T) {
	s := testSDE(t)
	require.True(t, s.IsShip(587), "Rifter is a ship") // category 6
	require.False(t, s.IsShip(2456), "a module/charge is not a ship")
}
