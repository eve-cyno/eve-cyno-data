package fit

// LegalitySDE is the SDE surface legality checks need (satisfied by *sde.SDE).
type LegalitySDE interface {
	GetRequiredSkillsFromDogma(typeID int) [][2]int
}

// Legality checks whether items are legal for a given clone state.
type Legality struct {
	sde LegalitySDE
	al  *AlphaAllowlist
}

// NewLegality constructs a Legality checker backed by the given SDE and Alpha allowlist.
func NewLegality(s LegalitySDE, al *AlphaAllowlist) *Legality {
	return &Legality{sde: s, al: al}
}

// IsAlphaLegalType reports whether an Alpha clone can use the given typeID: every
// skill the type requires must be on the Alpha allowlist (CCP's clone grades, see
// LoadAlphaAllowlist) at or above the required level.
//
// This is the whole rule; there is deliberately no meta-tier reject. The game gates
// Alpha clones by skills only. EVE University, "Clone states" (Alpha clones):
// https://wiki.eveuniversity.org/Clone_states#Alpha_clones - "Most Tech 2 modules are
// usable but not all", ""Navy" ships of Battleship size and below are usable, but not
// Tier 2 variants" (a Tier 2 hull needs a skill an Alpha cannot train), and the limits
// on battleship weapons, EWAR, drones and so on are the skill caps. So a Tech II or
// faction item is legal exactly when its skills are within the caps. (Deadspace and
// officer modules are likewise skill-gated as far as the sources say; the sources do
// not name an extra tier ban, so none is applied.)
func (l *Legality) IsAlphaLegalType(typeID int) bool {
	for _, rs := range l.sde.GetRequiredSkillsFromDogma(typeID) {
		skill, lvl := rs[0], rs[1]
		cap, ok := l.al.MaxLevel(skill)
		if !ok || lvl > cap {
			return false
		}
	}
	return true
}
