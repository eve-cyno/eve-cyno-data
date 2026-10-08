package sde

// GetDogma returns {attributeID: value} for the given typeID.
//
// EVE stores mass, volume and capacity as columns on invTypes rather than as
// dgmTypeAttributes rows, but every stat calculation treats them as dogma
// attributes 4 (mass) / 161 (volume) / 38 (capacity). We merge them in so the
// attribute map is complete — without mass (attr 4) the propulsion-module velocity formula and
// align-time calculation would see zero. Existing dgmTypeAttributes rows win
// (the rare type that carries an explicit attr overrides the type-record value).
func (s *SDE) GetDogma(typeID int) map[int]float64 {
	rows, err := s.db.Query(
		"SELECT attributeID, COALESCE(valueFloat, valueInt) AS val "+
			"FROM dgmTypeAttributes WHERE typeID = ?", typeID)
	if err != nil {
		return map[int]float64{}
	}
	defer rows.Close()
	result := make(map[int]float64)
	for rows.Next() {
		var attrID int
		var val *float64
		if err := rows.Scan(&attrID, &val); err != nil || val == nil {
			continue
		}
		result[attrID] = *val
	}

	// Merge base type-record columns (mass=4, capacity=38, volume=161) when the
	// type carries no explicit dogma attribute for them.
	var mass, capacity, volume *float64
	if err := s.db.QueryRow(
		"SELECT mass, capacity, volume FROM invTypes WHERE typeID = ?", typeID,
	).Scan(&mass, &capacity, &volume); err == nil {
		for attrID, v := range map[int]*float64{4: mass, 38: capacity, 161: volume} {
			if v == nil {
				continue
			}
			if _, ok := result[attrID]; !ok {
				result[attrID] = *v
			}
		}
	}
	return result
}

// GetGroupID returns groupID for a typeID, or nil if unknown.
func (s *SDE) GetGroupID(typeID int) *int {
	var gid int
	err := s.db.QueryRow("SELECT groupID FROM invTypes WHERE typeID = ?", typeID).Scan(&gid)
	if err != nil {
		return nil
	}
	return &gid
}

// GetModuleSlot returns the slot a module fits into based on its dogma effects.
// Result: "high" / "mid" / "low" / "rig" / "subsystem" / nil.
// Effect IDs: 12=hiPower, 13=medPower, 11=loPower, 2663=rigSlot, 3772=subSystem.
func (s *SDE) GetModuleSlot(typeID int) *string {
	rows, err := s.db.Query(
		"SELECT effectID FROM dgmTypeEffects WHERE typeID = ?", typeID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	effects := make(map[int]bool)
	for rows.Next() {
		var eid int
		if err := rows.Scan(&eid); err == nil {
			effects[eid] = true
		}
	}
	slot := ""
	switch {
	case effects[12]:
		slot = "high"
	case effects[13]:
		slot = "mid"
	case effects[11]:
		slot = "low"
	case effects[2663]:
		slot = "rig"
	case effects[3772]:
		slot = "subsystem"
	default:
		return nil
	}
	return &slot
}

// GetCategoryID returns categoryID for a typeID via invGroups join.
func (s *SDE) GetCategoryID(typeID int) *int {
	var catID int
	err := s.db.QueryRow(
		"SELECT g.categoryID FROM invTypes t JOIN invGroups g "+
			"ON t.groupID = g.groupID WHERE t.typeID = ?", typeID).Scan(&catID)
	if err != nil {
		return nil
	}
	return &catID
}

// GetSkillTypeIDs returns every published skill typeID (category 16).
// It is used by the dogma engine to build a full all-V skill set, i.e. the
// all-skills-at-V reference character that trains every skill to level 5.
func (s *SDE) GetSkillTypeIDs() []int {
	rows, err := s.db.Query(
		`SELECT t.typeID FROM invTypes t JOIN invGroups g ON t.groupID = g.groupID
		WHERE g.categoryID = 16 AND t.published = 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// IsDrone returns true when the typeID is in the Drone category (categoryID=18).
func (s *SDE) IsDrone(typeID int) bool {
	catID := s.GetCategoryID(typeID)
	return catID != nil && *catID == 18
}

// IsCharge returns true when the typeID is in the Charge category (categoryID=8).
func (s *SDE) IsCharge(typeID int) bool {
	catID := s.GetCategoryID(typeID)
	return catID != nil && *catID == 8
}
