package fit

// Damage patterns for effective hit points (E8).
//
// Source: the formula is plain arithmetic over the SDE resonances. A layer's effective
// HP against incoming damage split into the weights w (EM, thermal, kinetic, explosive,
// summing to 1) is
//
//	EHP_layer = HP / (w_em*r_em + w_therm*r_therm + w_kin*r_kin + w_exp*r_exp)
//
// with r the layer's resonance (1 - resist). The uniform pattern 25/25/25/25 is the mean
// of the four resonances, today's figure. EVE University wiki, "Effective HP" describes
// the same weighting in prose; no value in this file comes from that page.
//
// The generic presets are uniform and the four pure types. These are plain definitions
// (a split is 25/25/25/25 or 100 % of one type).
//
// The NPC faction presets (Angel Cartel, Blood Raiders, Guristas, Sansha's Nation,
// Serpentis, Rogue Drones) are the split of the damage a faction's ships deal, derived
// only from the SDE (build 3682781_20261110), so no third-party licence attaches to this
// file. The rule, which TestNPCDamagePatterns_DerivedFromSDE re-runs against the SDE:
//
//   - A faction's ships are the types in the NPC groups (category 11) named
//     "Asteroid <faction> <class>" or "Deadspace <faction> <class>" for frigates,
//     destroyers, cruisers, battlecruisers and battleships, commander variants included.
//     Officers, capitals, haulers and the rogue drone swarm are left out.
//   - A ship's damage per second, per damage type: turret (effect 10) damage attributes
//     114/118/117/116 x attribute 64 / attribute 51, plus missile (effect 569) the same
//     attributes of the charge in attribute 507 x attribute 212 / attribute 506.
//   - The faction's split is the sum over its ships per damage type, over the total,
//     rounded to whole percent by the largest-remainder rule.
//
// Limits: the SDE does not say which ships a site spawns or how often, so every ship
// type counts once and a preset is a faction average, not the profile of one mission.
// Turret and missile damage are added as if both always fire and hit. Guristas and Angel
// Cartel shift with ship size (Guristas battleships deal about 89 % kinetic, cruisers
// about 56 %), and Rogue Drones have no single profile: single drone types deal one or
// two damage types, and their preset is close to "everything, EM least".
//
// A new SDE build can move a preset by a point. The test then fails and prints the new
// split; changing a preset is a visible change of the EHP figures, so do it on purpose.

// DamagePatternUniform is the default pattern, the one used when no id is given.
const DamagePatternUniform = "uniform"

// DamagePattern is an incoming damage split. The four weights sum to 1.
type DamagePattern struct {
	ID    string
	EM    float64
	Therm float64
	Kin   float64
	Exp   float64
}

// damagePatterns holds the presets in a fixed order; the ids are the API ids.
var damagePatterns = []DamagePattern{
	{ID: DamagePatternUniform, EM: 0.25, Therm: 0.25, Kin: 0.25, Exp: 0.25},
	{ID: "em", EM: 1},
	{ID: "thermal", Therm: 1},
	{ID: "kinetic", Kin: 1},
	{ID: "explosive", Exp: 1},
	{ID: "angel", EM: 0.06, Therm: 0.10, Kin: 0.25, Exp: 0.59},
	{ID: "blood", EM: 0.52, Therm: 0.48},
	{ID: "guristas", Therm: 0.24, Kin: 0.76},
	{ID: "sansha", EM: 0.57, Therm: 0.42, Kin: 0.01},
	{ID: "serpentis", Therm: 0.62, Kin: 0.38},
	{ID: "rogueDrones", EM: 0.10, Therm: 0.24, Kin: 0.29, Exp: 0.37},
}

// DamagePatterns returns a copy of the presets in their fixed order.
func DamagePatterns() []DamagePattern {
	return append([]DamagePattern(nil), damagePatterns...)
}

// DamagePatternIDs returns the preset ids in their fixed order.
func DamagePatternIDs() []string {
	ids := make([]string, len(damagePatterns))
	for i, p := range damagePatterns {
		ids[i] = p.ID
	}
	return ids
}

// LookupDamagePattern returns the preset with the id. The empty id is uniform.
func LookupDamagePattern(id string) (DamagePattern, bool) {
	if id == "" {
		id = DamagePatternUniform
	}
	for _, p := range damagePatterns {
		if p.ID == id {
			return p, true
		}
	}
	return DamagePattern{}, false
}
