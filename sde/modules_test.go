package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchModules_HighSlotLauncher(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	hits := s.SearchModules("Rapid Light Missile Launcher", "high", 10)
	require.NotEmpty(t, hits)
	for _, h := range hits {
		require.Equal(t, "high", h.Slot)
		require.Contains(t, h.Name, "Rapid Light Missile Launcher")
		require.NotZero(t, h.TypeID)
	}
}

func TestSearchModules_SlotFilterExcludesOtherTiers(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	// Damage Control is a low-slot module — must not appear in a high search.
	low := s.SearchModules("Damage Control II", "low", 5)
	require.NotEmpty(t, low)
	require.Equal(t, "low", low[0].Slot)

	high := s.SearchModules("Damage Control II", "high", 5)
	require.Empty(t, high)
}

func TestModuleByID_Rifter1MN(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	// 2048 = Damage Control II (low slot).
	m := s.ModuleByID(2048)
	require.NotNil(t, m)
	require.Equal(t, "low", m.Slot)
	require.Equal(t, "Damage Control II", m.Name)
}
