package sde

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
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

func TestSearchItems_MetaSlotAndSize(t *testing.T) {
	s := testSDE(t)
	hits := s.SearchItems(ItemSearch{Query: "Shield Extender", Slot: "mid", MetaGroup: "tech ii"})
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.Name
		require.Equal(t, "mid", h.Slot)
		require.Equal(t, "Tech II", h.MetaTier)
	}
	require.Equal(t, []string{"Capital Shield Extender II", "Large Shield Extender II", "Medium Shield Extender II", "Small Shield Extender II"}, names)
	require.Equal(t, 45.0, hits[1].CPU)
	require.Equal(t, 160.0, hits[1].PG)

	large := s.SearchItems(ItemSearch{Query: "Shield Extender", Slot: "mid", MetaGroup: "Tech II", Size: "Large"})
	require.Len(t, large, 1)
	require.Equal(t, "Large Shield Extender II", large[0].Name)
}

func TestSearchItems_ChargesAndDrones(t *testing.T) {
	s := testSDE(t)
	charges := s.SearchItems(ItemSearch{Query: "Antimatter", Kind: "charge"})
	require.NotEmpty(t, charges)
	for _, h := range charges {
		require.Equal(t, "charge", h.Slot)
	}
	drones := s.SearchItems(ItemSearch{Query: "Hobgoblin", Kind: "drone", MetaGroup: "Tech I"})
	require.NotEmpty(t, drones)
	require.Equal(t, "drone", drones[0].Slot)
}

func TestMetaGroupNames(t *testing.T) {
	names := testSDE(t).MetaGroupNames()
	require.True(t, sort.StringsAreSorted(names))
	require.Contains(t, names, "Tech II")
	require.Contains(t, names, "Abyssal")
}

// TestSearchItems_MetaJoinUsesTheIndex pins that the meta group filter looks up
// invMetaTypes by its index instead of scanning it for every type.
func TestSearchItems_MetaJoinUsesTheIndex(t *testing.T) {
	s := testSDE(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN SELECT t.typeID FROM invTypes t"+metaGroupJoin+" WHERE t.typeName LIKE ?"+metaGroupWhere, "%er%", "tech i")
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	require.Contains(t, joined, "idx_invmetatypes_type", joined)
	require.NotContains(t, joined, "SCAN mt", joined)

	// Same hits as the unfiltered list narrowed by tier.
	all := s.SearchItems(ItemSearch{Query: "Shield Extender", Slot: "mid"})
	var want []int
	for _, h := range all {
		if h.MetaTier == "Tech II" {
			want = append(want, h.TypeID)
		}
	}
	var got []int
	for _, h := range s.SearchItems(ItemSearch{Query: "Shield Extender", Slot: "mid", MetaGroup: "Tech II"}) {
		got = append(got, h.TypeID)
	}
	require.Equal(t, want, got)
}

// TestSearchItems_LikeWildcardsAreLiteral pins that %, _ and a backslash in the
// query match only names that contain them.
func TestSearchItems_LikeWildcardsAreLiteral(t *testing.T) {
	s := testSDE(t)
	for _, q := range []string{"%%", "__", "%", `\`, `a\`} {
		require.Empty(t, s.SearchItems(ItemSearch{Query: q}), q)
		require.Empty(t, s.SearchItems(ItemSearch{Query: q, Kind: "drone"}), q)
		require.Empty(t, s.SearchModules(q, "", 10), q)
	}
	require.NotEmpty(t, s.SearchItems(ItemSearch{Query: "Shield Extender"}))
}

// TestSearchItemsPage_EnrichesOnlyThePage pins that the total counts every
// match while only the first limit hits carry dogma and meta facts.
func TestSearchItemsPage_EnrichesOnlyThePage(t *testing.T) {
	s := testSDE(t)
	f := ItemSearch{Query: "Shield Extender", Slot: "mid"}
	all := s.SearchItems(f)
	require.Greater(t, len(all), 3)
	page, total := s.SearchItemsPage(f, 3)
	require.Equal(t, len(all), total)
	require.Equal(t, all[:3], page)
	page, total = s.SearchItemsPage(f, 0)
	require.Empty(t, page)
	require.Equal(t, len(all), total)
	page, total = s.SearchItemsPage(f, 1000)
	require.Equal(t, all, page)
	require.Equal(t, len(all), total)
}

// TestGetItemMetaTier_UsesTheIndex pins that the meta tier lookup finds its
// invMetaTypes row by the index instead of scanning the table.
func TestGetItemMetaTier_UsesTheIndex(t *testing.T) {
	s := testSDE(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN SELECT mg.metaGroupName FROM invMetaTypes mt JOIN invMetaGroups mg ON CAST(mg.metaGroupID AS INTEGER) = CAST(mt.metaGroupID AS INTEGER) WHERE "+metaTierWhere, 587)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	require.Contains(t, joined, "idx_invmetatypes_type", joined)
	require.NotContains(t, joined, "SCAN mt", joined)
}
