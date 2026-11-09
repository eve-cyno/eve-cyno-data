package sde

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"

	"eve-cyno.dev/go/data/dogma"
)

// SubsetCategories are the SDE categories the fit engine reads: ships, modules, charges,
// skills, drones, implants/boosters and subsystems.
var SubsetCategories = []int{6, 7, 8, 16, 18, 20, 32}

// characterTypeID is the type a charID ItemModifier targets (invTypes 1373 "Character", category 1).
// Its category is outside SubsetCategories, so the exporter takes it as a full type on its own
// (subset format 2).
const characterTypeID = dogma.CharacterTypeID

// ExportSubset reads the engine's slice of the SDE (types of SubsetCategories, published or not,
// their attributes, effects, effect modifiers and attribute metadata) into a dogma.Subset.
// Attribute maps are the ones GetDogma returns, so the subset engine sees what the SQLite
// engine sees.
func (s *SDE) ExportSubset(sdeVersion string) (*dogma.Subset, error) {
	out := &dogma.Subset{Format: dogma.SubsetFormat, SDEVersion: sdeVersion}
	// Every type, published or not: ResolveNames resolves every invTypes row, so a cargo
	// line naming an unpublished type of any category resolves in both engines and the
	// fit's unresolved list, cargo and hash agree. Types of the engine's categories carry
	// their dogma below; the others are name-only entries. A type whose group is missing
	// still resolves through ResolveNames, so it is kept with category 0.
	rows, err := s.db.Query(`SELECT t.typeID, t.typeName, COALESCE(t.groupID, 0), COALESCE(g.categoryID, 0), t.published
		FROM invTypes t LEFT JOIN invGroups g ON t.groupID = g.groupID ORDER BY t.typeID`)
	if err != nil {
		return nil, fmt.Errorf("sde: export subset: types: %w", err)
	}
	var types []dogma.SubsetType
	for rows.Next() {
		var t dogma.SubsetType
		var name sql.NullString
		var published int
		if err := rows.Scan(&t.ID, &name, &t.Group, &t.Category, &published); err != nil {
			rows.Close()
			return nil, err
		}
		t.Name = name.String
		t.Unpublished = published != 1
		types = append(types, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	effectSeen := map[int]bool{}
	attrSeen := map[int]bool{}
	inScope := map[int]bool{}
	for _, c := range SubsetCategories {
		inScope[c] = true
	}
	meta, err := s.readSubsetMeta()
	if err != nil {
		return nil, err
	}
	for i := range types {
		t := &types[i]
		if !inScope[t.Category] && t.ID != characterTypeID {
			continue // name-only entry
		}
		if m, ok := meta[t.ID]; ok {
			t.MetaGroup, t.Parent = m.metaGroup, m.parent
		}
		t.Attrs = s.GetDogma(t.ID)
		if len(t.Attrs) == 0 {
			t.Attrs = nil
		}
		t.Effects = s.GetTypeEffectIDs(t.ID)
		for a := range t.Attrs {
			attrSeen[a] = true
		}
		for _, e := range t.Effects {
			effectSeen[e] = true
		}
	}
	effectIDs := make([]int, 0, len(effectSeen))
	for e := range effectSeen {
		effectIDs = append(effectIDs, e)
	}
	sort.Ints(effectIDs)
	for _, e := range effectIDs {
		mods := s.GetEffectModifiers(e)
		for _, m := range mods {
			attrSeen[m.ModifiedAttr] = true
			attrSeen[m.ModifyingAttr] = true
		}
		out.Effects = append(out.Effects, dogma.SubsetEffect{ID: e, Category: s.GetEffectCategory(e), Modifiers: mods})
	}
	attrIDs := make([]int, 0, len(attrSeen))
	for a := range attrSeen {
		attrIDs = append(attrIDs, a)
	}
	sort.Ints(attrIDs)
	for _, a := range attrIDs {
		m := s.GetAttributeMeta(a)
		out.Attributes = append(out.Attributes, dogma.SubsetAttr{ID: a, Name: m.Name, Stackable: m.Stackable, HighIsGood: m.HighIsGood, Default: m.Default})
	}
	out.Types = types
	return out, nil
}

type subsetMeta struct{ metaGroup, parent int }

// readSubsetMeta reads invMetaTypes (its ids are TEXT) into the meta group and the Tech I parent
// per type. A meta group of 1 is dropped: the SDE reads a type without a row as Tech I too, so
// the subset leaves both out. A type listed twice keeps its first row, as TypeFacts does. A
// file without the table gives no meta data.
func (s *SDE) readSubsetMeta() (map[int]subsetMeta, error) {
	out := map[int]subsetMeta{}
	if !s.HasTable("invMetaTypes") {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT typeID, metaGroupID, parentTypeID FROM invMetaTypes ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("sde: export subset: meta types: %w", err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var id, mg, parent sql.NullString
		if err := rows.Scan(&id, &mg, &parent); err != nil {
			return nil, err
		}
		tid, err := strconv.Atoi(id.String)
		if err != nil || seen[tid] {
			continue
		}
		seen[tid] = true
		m := subsetMeta{}
		if v, err := strconv.Atoi(mg.String); err == nil && v != 1 {
			m.metaGroup = max(v, 0)
		}
		if v, err := strconv.Atoi(parent.String); err == nil && v > 0 {
			m.parent = v
		}
		if m != (subsetMeta{}) {
			out[tid] = m
		}
	}
	return out, rows.Err()
}
