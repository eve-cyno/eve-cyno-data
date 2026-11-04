package sde

import (
	"database/sql"
	"fmt"

	"eve-cyno.dev/go/data/dogma"
)

// SubsetCategories are the SDE categories the fit engine reads: ships, modules, charges,
// skills, drones, implants/boosters and subsystems.
var SubsetCategories = []int{6, 7, 8, 16, 18, 20, 32}

// ExportSubset reads the engine's slice of the SDE (published types of SubsetCategories,
// their attributes, effects, effect modifiers and attribute metadata) into a dogma.Subset.
// Attribute maps are the ones GetDogma returns, so the subset engine sees what the SQLite
// engine sees.
func (s *SDE) ExportSubset(sdeVersion string) (*dogma.Subset, error) {
	out := &dogma.Subset{Format: dogma.SubsetFormat, SDEVersion: sdeVersion}
	rows, err := s.db.Query(`SELECT t.typeID, t.typeName, t.groupID, g.categoryID
		FROM invTypes t JOIN invGroups g ON t.groupID = g.groupID
		WHERE t.published = 1 AND g.categoryID IN (6,7,8,16,18,20,32) ORDER BY t.typeID`)
	if err != nil {
		return nil, fmt.Errorf("sde: export subset: types: %w", err)
	}
	var types []dogma.SubsetType
	for rows.Next() {
		var t dogma.SubsetType
		var name sql.NullString
		if err := rows.Scan(&t.ID, &name, &t.Group, &t.Category); err != nil {
			rows.Close()
			return nil, err
		}
		t.Name = name.String
		types = append(types, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	effectSeen := map[int]bool{}
	attrSeen := map[int]bool{}
	for i := range types {
		t := &types[i]
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
	for e := range effectSeen {
		mods := s.GetEffectModifiers(e)
		for _, m := range mods {
			attrSeen[m.ModifiedAttr] = true
			attrSeen[m.ModifyingAttr] = true
		}
		out.Effects = append(out.Effects, dogma.SubsetEffect{ID: e, Category: s.GetEffectCategory(e), Modifiers: mods})
	}
	for a := range attrSeen {
		m := s.GetAttributeMeta(a)
		out.Attributes = append(out.Attributes, dogma.SubsetAttr{ID: a, Name: m.Name, Stackable: m.Stackable, HighIsGood: m.HighIsGood})
	}
	out.Types = types
	return out, nil
}
