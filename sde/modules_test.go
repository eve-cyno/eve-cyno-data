package sde

import (
	"sort"
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

// TestSearchModules_AnySlotIsSortedByName pins the documented name order when no
// slot is given: one list across the slots, cut to limit by name, not the high
// slots first and the others after (or cut off).
func TestSearchModules_AnySlotIsSortedByName(t *testing.T) {
	s := testSDE(t)
	hits := s.SearchModules("Armor", "", 100)
	names := make([]string, len(hits))
	slots := map[string]bool{}
	for i, h := range hits {
		names[i] = h.Name
		slots[h.Slot] = true
	}
	require.True(t, sort.StringsAreSorted(names), "the doc promises name order across slots")
	require.Greater(t, len(slots), 1)

	first := s.SearchModules("Armor", "", 10)
	require.Len(t, first, 10)
	require.Equal(t, hits[:10], first, "a smaller limit keeps the first names")
}
