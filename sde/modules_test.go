package sde

import (
	"encoding/json"
	"path/filepath"
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

// TestSearch_QueryErrorIsAnEmptyList pins that a failed search answers an empty
// list, which /v1/items/search encodes as [], never null. The file here has no
// dgmTypeEffects or invGroups table, so every query fails.
func TestSearch_QueryErrorIsAnEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	writeRequiredSDE(t, path, nil)
	s, err := OpenWithReload(path, 0)
	require.NoError(t, err)
	defer s.Close()

	for name, hits := range map[string][]ModuleHit{
		"modules": s.SearchModules("Armor", "", 10),
		"slot":    s.SearchModules("Armor", "low", 10),
		"charges": s.SearchCharges("Antimatter", 10),
		"drones":  s.SearchDrones("Hobgoblin", 10),
	} {
		b, err := json.Marshal(hits)
		require.NoError(t, err)
		require.Equal(t, "[]", string(b), name)
	}
}
