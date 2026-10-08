package sde

import (
	"fmt"
	"math"
)

const (
	ActivityManufacturing = 1
	ActivityResearchTE    = 3
	ActivityResearchME    = 4
	ActivityCopying       = 5
	ActivityInvention     = 8
	ActivityReactions     = 11
)

// GetBlueprintForProduct finds the blueprint typeID that produces the given product (activityID=1).
func (s *SDE) GetBlueprintForProduct(productTypeID int) *int {
	if !s.HasTable("industryActivityProducts") {
		return nil
	}
	var bpID int
	err := s.db.QueryRow(
		"SELECT blueprint_typeID FROM industryActivityProducts "+
			"WHERE product_typeID = ? AND activityID = ? LIMIT 1",
		productTypeID, ActivityManufacturing).Scan(&bpID)
	if err != nil {
		return nil
	}
	return &bpID
}

// GetBlueprintMaterials returns [(material_typeID, quantity), ...] for one blueprint manufacturing job.
// Applies ME discount and run multiplier: actual_qty = max(runs, ceil(base_qty * runs * (1 - me/100))).
func (s *SDE) GetBlueprintMaterials(blueprintTypeID, runs, meLevel int) [][2]int {
	if !s.HasTable("industryActivityMaterials") {
		return nil
	}
	rows, err := s.db.Query(
		"SELECT material_typeID, quantity FROM industryActivityMaterials "+
			"WHERE blueprint_typeID = ? AND activityID = ?",
		blueprintTypeID, ActivityManufacturing)
	if err != nil {
		return nil
	}
	defer rows.Close()

	meFactor := math.Max(0.0, 1.0-float64(meLevel)/100.0)
	var result [][2]int
	for rows.Next() {
		var matID, baseQty int
		if err := rows.Scan(&matID, &baseQty); err != nil {
			continue
		}
		qty := int(math.Max(float64(runs), math.Ceil(float64(baseQty)*float64(runs)*meFactor)))
		result = append(result, [2]int{matID, qty})
	}
	return result
}

// GetBlueprintSkills returns [(skill_typeID, level), ...] required for the activity.
func (s *SDE) GetBlueprintSkills(blueprintTypeID, activityID int) [][2]int {
	if !s.HasTable("industryActivitySkills") {
		return nil
	}
	rows, err := s.db.Query(
		"SELECT skill_typeID, level FROM industryActivitySkills "+
			"WHERE blueprint_typeID = ? AND activityID = ?",
		blueprintTypeID, activityID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result [][2]int
	for rows.Next() {
		var skillID, level int
		if err := rows.Scan(&skillID, &level); err != nil {
			continue
		}
		result = append(result, [2]int{skillID, level})
	}
	return result
}

// GetRequiredSkillsFromDogma reads required skills + levels from dgmTypeAttributes.
// EVE encodes prereqs as paired attributes: (182,277), (183,278), (184,279),
// (1285,1286), (1289,1287), (1290,1288).
func (s *SDE) GetRequiredSkillsFromDogma(typeID int) [][2]int {
	attrs := s.GetDogma(typeID)
	pairs := [][2]int{
		{182, 277}, {183, 278}, {184, 279},
		{1285, 1286}, {1289, 1287}, {1290, 1288},
	}
	var result [][2]int
	for _, p := range pairs {
		skillID, hasSkill := attrs[p[0]]
		level, hasLevel := attrs[p[1]]
		if hasSkill && hasLevel {
			result = append(result, [2]int{int(skillID), int(level)})
		}
	}
	return result
}

// GetReprocessingYield returns [(material_typeID, quantity), ...] yielded by reprocessing.
// runs: portionSize multiplier. refiningEfficiencyPct: 0–100%.
func (s *SDE) GetReprocessingYield(typeID, runs int, refiningEfficiencyPct float64) [][2]int {
	if !s.HasTable("invTypeMaterials") {
		return nil
	}
	rows, err := s.db.Query(
		"SELECT materialTypeID, quantity FROM invTypeMaterials WHERE typeID = ?", typeID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	eff := math.Max(0.0, math.Min(1.0, refiningEfficiencyPct/100.0))
	var result [][2]int
	for rows.Next() {
		var matID, qty int
		if err := rows.Scan(&matID, &qty); err != nil {
			continue
		}
		result = append(result, [2]int{matID, int(math.Floor(float64(qty) * float64(runs) * eff))})
	}
	return result
}

// AlphaCloneSkills returns skill typeID → the highest level an Alpha clone can train it
// to, from CCP's clone grades (chrCloneGrades / chrCloneGradeSkills: one set of skills
// per Alpha race grade, levels stored as text). It errors, rather than returning an
// empty map, when the tables are missing or empty: callers treat the result as an
// allowlist, and an empty one would flag everything as Omega-only.
func (s *SDE) AlphaCloneSkills() (map[int]int, error) {
	if !s.HasTable("chrCloneGradeSkills") {
		return nil, fmt.Errorf("sde: table chrCloneGradeSkills is missing - rebuild the SDE (go -C ingest run ./cmd/ingest sde)")
	}
	rows, err := s.db.Query(`SELECT CAST(typeID AS INTEGER), MAX(CAST(level AS INTEGER)) FROM chrCloneGradeSkills GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("sde: read chrCloneGradeSkills: %w", err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var id, lvl int
		if err := rows.Scan(&id, &lvl); err != nil {
			return nil, fmt.Errorf("sde: read chrCloneGradeSkills: %w", err)
		}
		out[id] = lvl
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sde: read chrCloneGradeSkills: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sde: chrCloneGradeSkills has no rows")
	}
	return out, nil
}
