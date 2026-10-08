package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShipClassHulls_GroupName(t *testing.T) {
	s := testSDE(t)

	label, hulls := s.ShipClassHulls("black ops")
	require.Equal(t, "Black Ops", label)
	require.ElementsMatch(t, []string{"Redeemer", "Widow", "Sin", "Panther", "Marshal", "Python"}, hulls)

	label, hulls = s.ShipClassHulls("Heavy Assault Cruiser")
	require.Equal(t, "Heavy Assault Cruiser", label)
	require.Contains(t, hulls, "Muninn")
	require.Contains(t, hulls, "Ishtar")
	require.NotContains(t, hulls, "Rifter")
}

func TestShipClassHulls_CoarseClass(t *testing.T) {
	s := testSDE(t)

	label, hulls := s.ShipClassHulls("frigate")
	require.Equal(t, "frigate", label)
	require.Contains(t, hulls, "Rifter")
	require.Contains(t, hulls, "Retribution") // assault frigate: same coarse class
	require.NotContains(t, hulls, "Caracal")

	_, hulls = s.ShipClassHulls("cruiser")
	require.Contains(t, hulls, "Caracal")
	require.Contains(t, hulls, "Ishtar")
}

func TestShipClassHulls_Unknown(t *testing.T) {
	s := testSDE(t)
	label, hulls := s.ShipClassHulls("space whale")
	require.Empty(t, label)
	require.Empty(t, hulls)
	label, hulls = s.ShipClassHulls("")
	require.Empty(t, label)
	require.Empty(t, hulls)
}

func TestIsShipGroupName(t *testing.T) {
	s := testSDE(t)
	require.True(t, s.IsShipGroupName("Heavy Assault Cruiser"))
	require.True(t, s.IsShipGroupName("stealth bomber"))
	require.False(t, s.IsShipGroupName("Gila"))
	require.False(t, s.IsShipGroupName(""))
}
