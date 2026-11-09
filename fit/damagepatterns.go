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
// Shipped presets are the generic ones: uniform and the four pure types. These are plain
// definitions (a split is 25/25/25/25 or 100 % of one type), so no third-party licence
// attaches to this file. NPC faction presets (Angel, Blood Raiders, Guristas, Sansha,
// Serpentis, Rogue Drones, ...) are NOT shipped: their numbers have no source cleared
// by the clean-room rule (owner question Q19, open). Do not add them without that answer.

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
