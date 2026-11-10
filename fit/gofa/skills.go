package gofa

import (
	"eve-cyno.dev/go/data/fit"
)

// skillLevelFor returns the trained level for skillTypeID from a raw skill map.
// A nil map returns 5 (all-V default), the all-skills-at-V reference character
// the oracle values use. A missing entry returns 0 when missingAsZero is set
// (StatsOpts.MissingAsZero: an untrained skill), else 5.
func skillLevelFor(skills map[int]int, skillTypeID int, missingAsZero bool) int {
	if skills == nil {
		return 5
	}
	if lvl, ok := skills[skillTypeID]; ok {
		return lvl
	}
	if missingAsZero {
		return 0
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

// raiseAbsentHullSkills implements the game's behaviour for a skill that the hull or a fitted
// subsystem requires and the pilot does not have at all: the fitting simulation counts it as
// level I (owner decision 2026-11-10; observed on four numbers of the owner's in-game check
// 2026-11-10, no public source found). It acts only when the caller passed a profile,
// MissingAsZero and AbsentHullSkillAtI (a real character; the web, every all-V caller and an
// "all skills 0" profile never set it). A skill the
// profile lists, at any level including 0, is left alone, a trained level below the
// requirement is not raised, and module, rig and drone skills stay at 0 when absent.
func raiseAbsentHullSkills(opts fit.StatsOpts, items []*Item, skills map[int]int) {
	if opts.Skills == nil || !opts.MissingAsZero || !opts.AbsentHullSkillAtI {
		return
	}
	for _, it := range items {
		if it.Kind != KindShip && it.Kind != KindSubsystem {
			continue
		}
		for _, attr := range requiredSkillAttrs {
			id, ok := it.Attrs[attr]
			if !ok || id == 0 {
				continue
			}
			if _, listed := opts.Skills[int(id)]; !listed {
				skills[int(id)] = 1
			}
		}
	}
}
