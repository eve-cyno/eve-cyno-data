package sde

import (
	"database/sql"
	"encoding/json"
)

// Modifier represents one entry in the modifierInfo JSON array of a dgmEffects row.
type Modifier struct {
	Domain        string
	Func          string
	ModifiedAttr  int
	ModifyingAttr int
	Operation     int
	GroupID       *int // present for LocationGroupModifier
	SkillTypeID   *int // present for LocationRequiredSkillModifier
}

// modifierJSON mirrors the on-disk JSON structure for unmarshalling.
type modifierJSON struct {
	Domain               string `json:"domain"`
	Func                 string `json:"func"`
	ModifiedAttributeID  int    `json:"modifiedAttributeID"`
	ModifyingAttributeID int    `json:"modifyingAttributeID"`
	Operation            int    `json:"operation"`
	GroupID              *int   `json:"groupID"`
	SkillTypeID          *int   `json:"skillTypeID"`
}

// GetEffectModifiers returns the parsed Modifier slice for the given effectID.
// Returns nil on empty modifierInfo, parse error, or unknown effectID.
func (s *SDE) GetEffectModifiers(effectID int) []Modifier {
	var raw sql.NullString
	err := s.db.QueryRow(
		"SELECT modifierInfo FROM dgmEffects WHERE effectID = ?", effectID,
	).Scan(&raw)
	if err != nil || !raw.Valid || raw.String == "" {
		return nil
	}

	var entries []modifierJSON
	if err := json.Unmarshal([]byte(raw.String), &entries); err != nil {
		return nil
	}

	result := make([]Modifier, 0, len(entries))
	for _, e := range entries {
		result = append(result, Modifier{
			Domain:        e.Domain,
			Func:          e.Func,
			ModifiedAttr:  e.ModifiedAttributeID,
			ModifyingAttr: e.ModifyingAttributeID,
			Operation:     e.Operation,
			GroupID:       e.GroupID,
			SkillTypeID:   e.SkillTypeID,
		})
	}
	return result
}

// GetTypeEffectIDs returns all effectIDs for the given typeID from dgmTypeEffects.
// Returns nil on error or when no rows exist.
func (s *SDE) GetTypeEffectIDs(typeID int) []int {
	rows, err := s.db.Query(
		"SELECT effectID FROM dgmTypeEffects WHERE typeID = ?", typeID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var eid int
		if err := rows.Scan(&eid); err == nil {
			ids = append(ids, eid)
		}
	}
	return ids
}

// AttrMeta holds stackability, direction, and naming metadata for a dogma
// attribute (dgmAttributeTypes.stackable / highIsGood / attributeName).
type AttrMeta struct {
	Stackable  bool
	HighIsGood bool
	Name       string
}

// GetAttributeMeta returns stacking/direction/name metadata for the given attrID.
// The columns are listed in RequiredColumns; on a file that lacks them the query
// fails and the zero value is returned (the open-time check reports that at ERROR).
func (s *SDE) GetAttributeMeta(attrID int) AttrMeta {
	var stackable, highIsGood sql.NullInt64
	var name sql.NullString
	err := s.db.QueryRow(
		"SELECT stackable, highIsGood, attributeName FROM dgmAttributeTypes WHERE attributeID = ?", attrID,
	).Scan(&stackable, &highIsGood, &name)
	if err != nil {
		return AttrMeta{}
	}
	return AttrMeta{
		Stackable:  stackable.Int64 != 0,
		HighIsGood: highIsGood.Int64 != 0,
		Name:       name.String,
	}
}

// GetEffectCategory returns the dgmEffects.effectCategory for an effect:
// 0=passive, 1=active, 2=target, 3=area, 4=online, 5=overload, 6=dungeon,
// 7=system. Returns 0 (passive) on error or unknown effectID — a conservative
// default that keeps the effect in scope rather than silently dropping it.
func (s *SDE) GetEffectCategory(effectID int) int {
	var cat sql.NullInt64
	if err := s.db.QueryRow(
		"SELECT effectCategory FROM dgmEffects WHERE effectID = ?", effectID,
	).Scan(&cat); err != nil {
		return 0
	}
	return int(cat.Int64)
}
