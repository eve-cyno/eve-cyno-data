package gofa

import (
	"database/sql"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"eve-cyno.dev/go/data/sde/sdetest"
)

// T2 (plan 2026-11-10-fit-implant-sets): the implant sets derived from the SDE. Real-SDE tests
// on build 3670638_20261104: 56 set-grade entries, 334 implants.

const (
	wantImplantSets    = 56
	wantImplantMembers = 334
)

// implantFixture is the published implant catalogue and the derived sets, loaded once per test
// binary (each load opens the SDE). It is filled on the first successful call; a call that skips
// (no SDE) leaves it empty so the next test tries again.
var implantFixture struct {
	mu    sync.Mutex
	sets  []ImplantSet
	ids   []int
	names map[int]string
	db    *sql.DB // read-only, shared, never closed (the test binary exits)
}

// publishedImplants reads the published category-20 type ids and names straight from the SDE
// file: gofa.SDE cannot list types, so the host passes this catalogue in production.
func publishedImplants(t *testing.T, db *sql.DB) ([]int, map[int]string) {
	t.Helper()
	rows, err := db.Query(`SELECT t.typeID, t.typeName FROM invTypes t JOIN invGroups g ON g.groupID = t.groupID
		WHERE g.categoryID = 20 AND t.published = 1 ORDER BY t.typeID`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []int
	names := map[int]string{}
	for rows.Next() {
		var id int
		var n string
		require.NoError(t, rows.Scan(&id, &n))
		ids = append(ids, id)
		names[id] = n
	}
	require.NoError(t, rows.Err())
	return ids, names
}

// loadImplantSetsAll returns the shared fixture: sets, the catalogue ids and names, the db.
func loadImplantSetsAll(t *testing.T) ([]ImplantSet, []int, map[int]string, *sql.DB) {
	t.Helper()
	f := &implantFixture
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sets == nil {
		db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
		require.NoError(t, err)
		ids, names := publishedImplants(t, db)
		s := openRealSDE(t)
		defer s.Close()
		f.sets = ImplantSets(s, ids, func(id int) string { return names[id] })
		f.ids, f.names, f.db = ids, names, db
	}
	return f.sets, f.ids, f.names, f.db
}

func loadImplantSets(t *testing.T) ([]ImplantSet, *sql.DB) {
	t.Helper()
	sets, _, _, db := loadImplantSetsAll(t)
	return sets, db
}

// The key table equals every attribute named implantSet*, ImplantSet* or setBonus* minus the two
// Wedge helpers. A new matching attribute fails here until it is classified.
func TestImplantSets_KeyTableComplete(t *testing.T) {
	skipHeavyRace(t)
	_, db := loadImplantSets(t)
	rows, err := db.Query(`SELECT attributeID, attributeName FROM dgmAttributeTypes
		WHERE attributeName LIKE 'implantSet%' OR attributeName LIKE 'setBonus%' ORDER BY attributeID`)
	require.NoError(t, err)
	defer rows.Close()
	var matched []int
	for rows.Next() {
		var id int
		var name string
		require.NoError(t, rows.Scan(&id, &name))
		lower := strings.ToLower(name)
		require.True(t, strings.HasPrefix(lower, "implantset") || strings.HasPrefix(lower, "setbonus"), name)
		matched = append(matched, id)
	}
	require.NoError(t, rows.Err())
	require.Len(t, matched, len(ImplantSetKeyAttrs)+len(ImplantSetHelperAttrs),
		"a new implantSet*/setBonus* attribute appeared: add it to ImplantSetKeyAttrs or ImplantSetHelperAttrs")
	want := append(slices.Clone(ImplantSetKeyAttrs), ImplantSetHelperAttrs...)
	slices.Sort(want)
	require.Equal(t, want, matched)
	require.Len(t, ImplantSetKeyAttrs, 27)
	require.True(t, slices.IsSorted(ImplantSetKeyAttrs), "the lowest-key rule needs ascending keys")
}

// No published implant carries two set keys (ImplantSets takes the lowest; two would hide one).
func TestImplantSets_OneKeyPerImplant(t *testing.T) {
	skipHeavyRace(t)
	_, ids, _, _ := loadImplantSetsAll(t)
	s := openRealSDE(t)
	defer s.Close()
	for _, id := range ids {
		n := 0
		for _, k := range ImplantSetKeyAttrs {
			if _, ok := s.GetDogma(id)[k]; ok {
				n++
			}
		}
		require.LessOrEqual(t, n, 1, "implant %d carries %d set keys", id, n)
	}
}

// Every member's name starts with the set's name ("Mid-grade Crystal" -> "Mid-grade Crystal
// Alpha"; Genolution -> "Genolution ..."), and Family/Name come from the lowest type id.
func TestImplantSets_NamePrefix(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	for _, set := range sets {
		lowest := set.Members[0].TypeID
		for _, m := range set.Members {
			require.True(t, strings.HasPrefix(m.Name, set.Name+" "), "%q is not named after set %q", m.Name, set.Name)
			lowest = min(lowest, m.TypeID)
		}
		_, fam := splitSetName(set.Members[lowestIndex(set.Members, lowest)].Name)
		require.Equal(t, fam, set.Family, set.Name)
	}
}

func lowestIndex(ms []ImplantSetMember, id int) int {
	for i, m := range ms {
		if m.TypeID == id {
			return i
		}
	}
	return -1
}

// ImplantSlot: attribute 331 as an integer 1..10; AU-79 (slot 79) and a non-implant are false.
func TestImplantSlot(t *testing.T) {
	skipHeavyRace(t)
	_, _, names, _ := loadImplantSetsAll(t)
	s := openRealSDE(t)
	defer s.Close()
	slot, ok := ImplantSlot(s, 22107) // Mid-grade Crystal Alpha
	require.True(t, ok)
	require.Equal(t, 1, slot)
	slot, ok = ImplantSlot(s, 22112) // Omega
	require.True(t, ok)
	require.Equal(t, 6, slot)
	au79 := 0
	for id, n := range names {
		if strings.Contains(n, "AU-79") {
			au79 = id
		}
	}
	require.NotZero(t, au79, "AU-79 is in the catalogue")
	require.Equal(t, 79.0, s.GetDogma(au79)[attrImplantness])
	_, ok = ImplantSlot(s, au79)
	require.False(t, ok)
	_, ok = ImplantSlot(s, 587) // Rifter
	require.False(t, ok)
	_, ok = ImplantSlot(s, -1)
	require.False(t, ok)
}

func TestImplantSets_Counts(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	members := 0
	for _, set := range sets {
		members += len(set.Members)
	}
	require.Len(t, sets, wantImplantSets)
	require.Equal(t, wantImplantMembers, members)
}

// Every grade word is High, Mid or Low (or none); the key table covers every published implant
// that carries a key attribute (none is lost to a slot or name rule).
func TestImplantSets_GradesAndCoverage(t *testing.T) {
	skipHeavyRace(t)
	sets, db := loadImplantSets(t)
	for _, set := range sets {
		require.Contains(t, []string{"High", "Mid", "Low", ""}, set.Grade, "%s: grade word", set.Name)
		// Only the Genolution set (key 1799, attribute implantSetChristmas) has no grade word.
		require.Equal(t, set.Key == 1799, set.Grade == "", "%s: ungraded only for key 1799", set.Name)
		require.NotEmpty(t, set.Family, set.Name)
	}
	in := map[int]bool{}
	for _, set := range sets {
		for _, m := range set.Members {
			in[m.TypeID] = true
		}
	}
	rows, err := db.Query(`SELECT DISTINCT t.typeID, t.typeName FROM invTypes t
		JOIN dgmTypeAttributes a ON a.typeID = t.typeID
		JOIN dgmAttributeTypes n ON n.attributeID = a.attributeID
		WHERE t.published = 1 AND (n.attributeName LIKE 'implantSet%' OR n.attributeName LIKE 'setBonus%')
		AND n.attributeID NOT IN (5799, 5800)`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		require.NoError(t, rows.Scan(&id, &name))
		require.True(t, in[id], "%d %s carries a set key but is in no set", id, name)
	}
	require.NoError(t, rows.Err())
}

// Slots are within 1..10, a member's slot is its attribute 331, and no implant is in two sets.
func TestImplantSets_SlotsAndUniqueness(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	s := openRealSDE(t)
	defer s.Close()
	owner := map[int]string{}
	names := map[string]bool{}
	for _, set := range sets {
		require.False(t, names[set.Name], "duplicate set name %s", set.Name)
		names[set.Name] = true
		require.NotEmpty(t, set.Slots, set.Name)
		require.True(t, slices.IsSorted(set.Slots), set.Name)
		bySlot := map[int]int{}
		for _, m := range set.Members {
			require.GreaterOrEqual(t, m.Slot, 1, m.Name)
			require.LessOrEqual(t, m.Slot, 10, m.Name)
			require.Equal(t, float64(m.Slot), s.GetDogma(m.TypeID)[attrImplantness], m.Name)
			require.Contains(t, set.Slots, m.Slot)
			bySlot[m.Slot]++
			prev, dup := owner[m.TypeID]
			require.False(t, dup, "%d %s is in %q and %q", m.TypeID, m.Name, prev, set.Name)
			owner[m.TypeID] = set.Name
		}
		require.Len(t, bySlot, len(set.Slots), "%s: one member per slot", set.Name)
		for slot, n := range bySlot {
			require.Equal(t, 1, n, "%s slot %d", set.Name, slot)
		}
	}
}

// Sorted by family, then grade high, mid, low.
func TestImplantSets_Order(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	rank := map[string]int{"High": 0, "Mid": 1, "Low": 2, "": 3}
	for i := 1; i < len(sets); i++ {
		a, b := sets[i-1], sets[i]
		if a.Family == b.Family {
			require.Less(t, rank[a.Grade], rank[b.Grade], "%s before %s", a.Name, b.Name)
		} else {
			require.Less(t, a.Family, b.Family)
		}
	}
	var crystal []string
	for _, set := range sets {
		if set.Family == "Crystal" {
			crystal = append(crystal, set.Name)
		}
	}
	require.Equal(t, []string{"High-grade Crystal", "Mid-grade Crystal", "Low-grade Crystal"}, crystal)
}

// Mid-grade Crystal: Alpha 22107 (slot 1) to Omega 22112 (slot 6), all modelled; the Genolution
// set has no grade and slots 1-4.
func TestImplantSets_KnownSets(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	find := func(name string) ImplantSet {
		for _, set := range sets {
			if set.Name == name {
				return set
			}
		}
		t.Fatalf("no set %q", name)
		return ImplantSet{}
	}
	c := find("Mid-grade Crystal")
	require.Equal(t, "Mid", c.Grade)
	require.Equal(t, "Crystal", c.Family)
	require.Equal(t, 838, c.Key)
	require.Equal(t, []int{1, 2, 3, 4, 5, 6}, c.Slots)
	var ids []int
	for _, m := range c.Members {
		require.True(t, m.Modelled, m.Name)
		ids = append(ids, m.TypeID)
	}
	require.ElementsMatch(t, midGradeCrystals, ids)
	require.Equal(t, 22107, c.Members[0].TypeID)
	require.Equal(t, "Mid-grade Crystal Alpha", c.Members[0].Name)

	g := find("Genolution")
	require.Equal(t, "", g.Grade)
	require.Equal(t, []int{1, 2, 3, 4}, g.Slots)
}

// Every implant of every set is modelled today (plan section 2); a regression here means the
// engine would silently skip a set member.
func TestImplantSets_AllModelled(t *testing.T) {
	skipHeavyRace(t)
	sets, _ := loadImplantSets(t)
	for _, set := range sets {
		for _, m := range set.Members {
			require.True(t, m.Modelled, "%s: %d %s", set.Name, m.TypeID, m.Name)
		}
	}
}

// Name parsing: an unknown grade word is returned, not hidden.
func TestSplitSetName(t *testing.T) {
	g, f := splitSetName("Mid-grade Crystal Alpha")
	require.Equal(t, "Mid", g)
	require.Equal(t, "Crystal", f)
	g, f = splitSetName("Ultra-grade Crystal Alpha")
	require.Equal(t, "Ultra", g)
	require.Equal(t, "Crystal", f)
	g, f = splitSetName("mid-GRADE Crystal Alpha")
	require.Equal(t, "mid", g)
	require.Equal(t, "Crystal", f)
	g, f = splitSetName("-grade Foo")
	require.Equal(t, "", g)
	require.Equal(t, "-grade", f)
	g, f = splitSetName("Genolution Core Augmentation CA-1")
	require.Equal(t, "", g)
	require.Equal(t, "Genolution", f)
}
