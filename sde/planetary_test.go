package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchematicFor_Robotics(t *testing.T) {
	s := testSDE(t)
	sc, ok := s.SchematicFor(9848)
	require.True(t, ok)
	require.Equal(t, 97, sc.ID)
	require.Equal(t, 3, sc.OutputPerCycle)
	require.Equal(t, 3600, sc.CycleSeconds)
	require.ElementsMatch(t, [][2]int{{3689, 10}, {9836, 10}}, sc.Inputs)
	require.Equal(t, 3, s.PlanetaryTier(9848))
	require.Equal(t, 0, s.PlanetaryTier(2267)) // Base Metals
	require.Equal(t, -1, s.PlanetaryTier(34))
	_, ok = s.SchematicFor(34)
	require.False(t, ok, "Tritanium has no schematic")
}
