package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func variantNames(t *testing.T, s *SDE, ids []int) []string {
	t.Helper()
	names := s.GetTypeNames(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, names[id])
	}
	return out
}

// TestMetaVariants_ShieldExtenderFamily: the family of Large Shield Extender II is
// its Tech I parent plus every sibling sharing that parent; the type itself is
// excluded, and a Tech I base item sees the same family.
func TestMetaVariants_ShieldExtenderFamily(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	ids := s.ResolveNames([]string{"Large Shield Extender II", "Large Shield Extender I"})
	t2, t1 := ids["Large Shield Extender II"], ids["Large Shield Extender I"]
	require.NotZero(t, t2)
	require.NotZero(t, t1)

	fromT2 := variantNames(t, s, s.MetaVariants(t2))
	require.NotContains(t, fromT2, "Large Shield Extender II", "the type itself is excluded")
	require.Contains(t, fromT2, "Large Shield Extender I", "the Tech I parent is part of the family")
	require.Contains(t, fromT2, "Large F-S9 Regolith Compact Shield Extender")
	require.Contains(t, fromT2, "Caldari Navy Large Shield Extender", "faction siblings are returned; callers filter by meta tier")
	for _, n := range fromT2 {
		require.NotContains(t, n, "Medium Shield Extender", "a different size is a different family")
		require.NotContains(t, n, "Small Shield Extender", "a different size is a different family")
	}

	fromT1 := variantNames(t, s, s.MetaVariants(t1))
	require.Contains(t, fromT1, "Large Shield Extender II")
	require.NotContains(t, fromT1, "Large Shield Extender I")
}

func TestMetaVariants_NoFamily(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	require.Empty(t, s.MetaVariants(0))
	// A Tritanium has no meta family.
	id := s.ResolveNames([]string{"Tritanium"})["Tritanium"]
	require.NotZero(t, id)
	require.Empty(t, s.MetaVariants(id))
}

// TestNearestModuleNames_RenamedModules pins the hint for names CCP renamed: the
// old name is not in the SDE as a published item, the new one is.
func TestNearestModuleNames_RenamedModules(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	cases := []struct {
		bad  string
		want []string // every entry must be among the first len(want)+1 hints
	}{
		{"Energized Adaptive Nano Membrane II", []string{"Multispectrum Energized Membrane II"}},
		{"Scan Probe Launcher II", []string{"Core Probe Launcher II", "Expanded Probe Launcher II"}},
		{"Micro Capacitor Booster II", []string{"Small Capacitor Booster II"}},
	}
	for _, tc := range cases {
		t.Run(tc.bad, func(t *testing.T) {
			got := s.NearestModuleNames(tc.bad, 3)
			require.NotEmpty(t, got)
			require.LessOrEqual(t, len(got), 3)
			for _, w := range tc.want {
				require.Contains(t, got, w, "hints: %v", got)
			}
			require.NotContains(t, got, tc.bad)
		})
	}
}

func TestNearestModuleNames_PublishedItemsOnly(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	for _, bad := range []string{"Energized Adaptive Nano Membrane II", "Scan Probe Launcher II", "50mm Steel Plates II"} {
		for _, name := range s.NearestModuleNames(bad, 5) {
			id := s.ResolveNames([]string{name})[name]
			require.NotZero(t, id, "%q must resolve to a published item", name)
			var published int
			require.NoError(t, s.db.QueryRow(`SELECT published FROM invTypes WHERE typeID=?`, id).Scan(&published))
			require.Equal(t, 1, published, "%q is unpublished", name)
		}
	}
}

// TestNearestModuleNames_PrefersSameTier: the " II" query ranks the " II" sibling
// of a family ahead of its " I" sibling.
func TestNearestModuleNames_PrefersSameTier(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	got := s.NearestModuleNames("Medium Armour Repairer II", 3)
	require.NotEmpty(t, got)
	require.Equal(t, "Medium Armor Repairer II", got[0], "hints: %v", got)
}

func TestNearestModuleNames_EmptyAndGarbage(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	defer s.Close()

	require.Empty(t, s.NearestModuleNames("", 3))
	require.Empty(t, s.NearestModuleNames("   ", 3))
	require.Empty(t, s.NearestModuleNames("Zzyzx Qwertyuiop", 3))
}
