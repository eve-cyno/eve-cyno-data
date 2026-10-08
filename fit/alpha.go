package fit

import "fmt"

// AlphaSkillSource supplies the Alpha clone skill caps (satisfied by *sde.SDE).
type AlphaSkillSource interface {
	// AlphaCloneSkills returns skill typeID → the highest level any Alpha clone grade
	// can train it to.
	AlphaCloneSkills() (map[int]int, error)
}

// AlphaAllowlist maps skill typeID → max level an Alpha clone can train.
type AlphaAllowlist struct {
	byTypeID map[int]int
}

// LoadAlphaAllowlist builds the allowlist from CCP's own Alpha clone grades in the SDE
// (chrCloneGrades / chrCloneGradeSkills), so it follows the game instead of a hand-kept
// list. A source that fails or has no skills is an error: an empty allowlist would
// silently make every module with a skill requirement Alpha-illegal.
func LoadAlphaAllowlist(src AlphaSkillSource) (*AlphaAllowlist, error) {
	skills, err := src.AlphaCloneSkills()
	if err != nil {
		return nil, fmt.Errorf("load alpha clone skills: %w", err)
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("load alpha clone skills: the SDE lists none")
	}
	return &AlphaAllowlist{byTypeID: skills}, nil
}

// MaxLevel returns the maximum level an Alpha clone can train the given skill typeID,
// and whether the skill is on the allowlist at all.
func (a *AlphaAllowlist) MaxLevel(skillTypeID int) (int, bool) {
	lvl, ok := a.byTypeID[skillTypeID]
	return lvl, ok
}
