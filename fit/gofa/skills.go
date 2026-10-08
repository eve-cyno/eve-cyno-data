package gofa

import (
	"eve-cyno.dev/go/data/fit"
)

// skillLevelFor returns the trained level for skillTypeID from a raw skill map.
// A nil map or a missing entry returns 5 (all-V default), the all-skills-at-V
// reference character the oracle values use.
func skillLevelFor(skills map[int]int, skillTypeID int) int {
	if skills == nil {
		return 5
	}
	if lvl, ok := skills[skillTypeID]; ok {
		return lvl
	}
	return 5
}

// DefaultSkills builds a SkillProfile that trains every skill required by the
// supplied typeIDs to level 5. It reads the required-skill typeID attrs encoded
// as dogma attribute pairs:
//
//	(182, 277)  requiredSkill1 typeID / minimum level
//	(183, 278)  requiredSkill2 typeID / minimum level
//	(184, 279)  requiredSkill3 typeID / minimum level
//	(1285,1286) requiredSkill4 typeID / minimum level
//	(1289,1287) requiredSkill5 typeID / minimum level
//	(1290,1288) requiredSkill6 typeID / minimum level
//
// The result can be passed directly as fit.StatsOpts.Skills. Types with no
// required-skill attrs are silently skipped.
func DefaultSkills(s SDE, typeIDs []int) fit.SkillProfile {
	profile := make(fit.SkillProfile)
	// Only the typeID attrs matter here; the minimum-level column is ignored
	// because we always default to level 5 (all-V).
	skillAttrIDs := []int{182, 183, 184, 1285, 1289, 1290}
	for _, typeID := range typeIDs {
		attrs := s.GetDogma(typeID)
		for _, attrID := range skillAttrIDs {
			v, ok := attrs[attrID]
			if !ok {
				continue
			}
			skillTypeID := int(v)
			if _, already := profile[skillTypeID]; !already {
				profile[skillTypeID] = 5
			}
		}
	}
	return profile
}
