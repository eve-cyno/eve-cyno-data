package sde

import "math"

// Skill dogma attributes (EVE University, "Skills and learning").
const (
	attrSkillTimeConstant  = 275 // the skill's rank
	attrPrimaryAttribute   = 180 // attribute id of the primary training attribute
	attrSecondaryAttribute = 181 // attribute id of the secondary training attribute
	skillPointsBase        = 250 // SP of level I at rank 1
	skillPointsLevelFactor = 32  // SP grows by 32^((level-1)/2)
)

// SkillTraining is the training data of one skill: its rank and its two attributes.
type SkillTraining struct {
	SkillID int
	// Rank is dogma attribute 275, skillTimeConstant.
	Rank float64
	// Primary is the attribute id of the primary training attribute (164 charisma, 165
	// intelligence, 166 memory, 167 perception, 168 willpower; dogma 180).
	Primary int
	// Secondary is the attribute id of the secondary training attribute (dogma 181).
	Secondary int
}

// SkillTraining reads a skill's rank and training attributes. It reports false for a type
// without a rank or without both attributes (not a skill).
func (s *SDE) SkillTraining(skillID int) (SkillTraining, bool) {
	attrs := s.GetDogma(skillID)
	rank, hasRank := attrs[attrSkillTimeConstant]
	primary, hasPrimary := attrs[attrPrimaryAttribute]
	secondary, hasSecondary := attrs[attrSecondaryAttribute]
	if !hasRank || !hasPrimary || !hasSecondary || rank <= 0 {
		return SkillTraining{}, false
	}
	return SkillTraining{SkillID: skillID, Rank: rank, Primary: int(primary), Secondary: int(secondary)}, true
}

// SkillPointsFor is the SP of a skill trained from zero to level (EVE University, "Skills and
// learning"): round(250 x rank x 32^((level-1)/2)), half up; 0 for level 0 or below.
func SkillPointsFor(rank float64, level int) int {
	if level < 1 {
		return 0
	}
	sp := skillPointsBase * rank * math.Pow(skillPointsLevelFactor, float64(level-1)/2)
	return int(math.Floor(sp + 0.5))
}
