package sde

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/dogma"
)

// SubsetCategories are the SDE categories the fit engine reads: ships, modules, charges,
// skills, drones, implants/boosters and subsystems.
var SubsetCategories = []int{6, 7, 8, 16, 18, 20, 32}

// ExportSubset reads the engine's slice of the SDE (types of SubsetCategories, published or not,
// their attributes, effects, effect modifiers and attribute metadata) into a dogma.Subset.
// Attribute maps are the ones GetDogma returns, so the subset engine sees what the SQLite
// engine sees.
func (s *SDE) ExportSubset(sdeVersion string) (*dogma.Subset, error) {
	out := &dogma.Subset{Format: dogma.SubsetFormat, SDEVersion: sdeVersion}
	// Types of the engine's categories, published or not (the SQLite SDE resolves
	// unpublished names and serves their dogma), plus published types of other categories
	// as name-only entries so cargo lines resolve.
	rows, err := s.db.Query(`SELECT t.typeID, t.typeName, t.groupID, g.categoryID, t.published
		FROM invTypes t JOIN invGroups g ON t.groupID = g.groupID
		WHERE g.categoryID IN (` + inList(SubsetCategories) + `) OR t.published = 1 ORDER BY t.typeID`)
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
	for i := range types {
		t := &types[i]
		if !inScope[t.Category] {
			continue // name-only entry
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
		out.Attributes = append(out.Attributes, dogma.SubsetAttr{ID: a, Name: m.Name, Stackable: m.Stackable, HighIsGood: m.HighIsGood})
	}
	out.Types = types
	return out, nil
}

func inList(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}
