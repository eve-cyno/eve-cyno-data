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
// only from the SDE (build 3682781_20261110). No other source is used, so no licence
// attaches to them beyond the EVE Developer License Agreement that covers SDE-derived
// data (see NOTICE). The rule, which TestNPCDamagePatterns_DerivedFromSDE re-runs
// against the SDE:
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
// type counts once and a preset is a DPS-weighted faction average (battleship types
// carry nearly half of it), not the profile of one mission. Turret and missile damage
// are added as if both always fire and hit. Guristas and Angel Cartel shift with ship
// size (on that build Guristas battleships deal about 89 % kinetic, cruisers about
// 56 %), and Rogue Drones have no single profile: most single drone types deal one or
// two damage types, and their preset is close to "everything, EM least".
//
// The Abyss presets (ids starting "abyss") are the damage split of one faction's ships in
// Abyssal Deadspace, derived from the SDE (build 3682781_20261110) by this rule, which
// TestAbyssDamagePatterns_DerivedFromSDE re-runs:
//
//   - The types are the ones the abyss package lists (SDE groups 1982 and 1997).
//   - A type's damage per second, per damage type, is the sum of its weapon effects:
//     turret (effect 10), disintegrator (effect 6995) and vorton (effect 8088), each the
//     damage attributes 114/118/117/116 x attribute 64 / attribute 51, plus missile
//     (effect 569) the same attributes of the charge in attribute 507 x attribute 212 /
//     attribute 506. The disintegrator ramp and the vorton chain to further targets are
//     left out: they scale all four damage types alike.
//   - A fixed table groups the abyss package's families into nine factions: Triglavian
//     (Damavik, Vila Damavik, Kikimora, Vedmak, Vila Vedmak, Drekavac, Leshak, Rodiva,
//     Vila Swarmer), Drifters (Drifter, Ephialtes, Tyrannos), Sleepers (Lucid), Rogue
//     Drones (Tessella, Tessera), Abyssal Overmind, Sansha's Nation (Devoted), Angel
//     Cartel (Lucifer), CONCORD and EDENCOM. The last two both carry the family name
//     "Disparu Troop"; a type with the vorton effect is EDENCOM, one with missiles is
//     CONCORD. The faction names and which family belongs to which faction follow EVE
//     University, "Abyssal Deadspace" (https://wiki.eveuniversity.org/Abyssal_Deadspace,
//     CC BY-SA 4.0, see NOTICE). No number comes from that page.
//   - The split is the faction's summed damage per second per type over the total,
//     rounded to whole percent by the largest-remainder rule.
//   - Left out: Awoken Preserver and Igniting Swarmer, because that page names neither,
//     so their faction is not verified. Tyrannos Typhon is grouped with the Drifters by
//     its name only; it deals 25/25/25/25 and does not move the Drifter split.
//
// Limits of the Abyss presets: every type counts once, since the SDE does not say which
// ships a pocket spawns; the Abyss Sansha and Angel presets are separate from the PvE
// ones because the ships differ; a room of Tessellas or Tesseras of one kind deals one
// damage type (named in the type), so the pure type fits it better than abyssRogueDrones;
// weather changes resists, not the incoming split, and the SDE has no numbers for it.
//
// The PvP presets (ids starting "pvp") are the split of one named L-size charge: its damage
// attributes 114/118/117/116 over their sum, in whole percent by the same rule.
// TestPvPDamagePatterns_DerivedFromSDE checks the charge type id, its name and the split.
// Lasers and hybrids deal a fixed pair of types; projectile and disintegrator ammo deals
// different types per ammo, so the common ammos get one preset each. Missiles and combat
// drones deal one type each, which the pure general presets cover. Other sizes and the
// faction variants of an ammo share its split (checked for two of them); an ammo with no
// preset (Conflagration, Gleam, Javelin, Null, Hail, Quake) is within a few points of one
// that has. No fleet composition is modelled: a custom weight set covers a known enemy.
//
// A new SDE build can move a preset by a point. The test then fails and prints the new
// split; changing a preset is a visible change of the EHP figures, so do it on purpose.

// DamagePatternUniform is the default pattern, the one used when no id is given.
const DamagePatternUniform = "uniform"

// DamagePattern is an incoming damage split. The four weights sum to 1.
type DamagePattern struct {
	ID string
	// Category groups the presets for a picker: general, pve, abyss or pvp. A custom
	// weight set (StatsOpts.DamageWeights) has none.
	Category string
	EM       float64
	Therm    float64
	Kin      float64
	Exp      float64
}

// damagePatterns holds the presets in a fixed order; the ids are the API ids.
var damagePatterns = []DamagePattern{
	{ID: DamagePatternUniform, Category: "general", EM: 0.25, Therm: 0.25, Kin: 0.25, Exp: 0.25},
	{ID: "em", Category: "general", EM: 1},
	{ID: "thermal", Category: "general", Therm: 1},
	{ID: "kinetic", Category: "general", Kin: 1},
	{ID: "explosive", Category: "general", Exp: 1},
	{ID: "angel", Category: "pve", EM: 0.06, Therm: 0.10, Kin: 0.25, Exp: 0.59},
	{ID: "blood", Category: "pve", EM: 0.52, Therm: 0.48},
	{ID: "guristas", Category: "pve", Therm: 0.24, Kin: 0.76},
	{ID: "sansha", Category: "pve", EM: 0.57, Therm: 0.42, Kin: 0.01},
	{ID: "serpentis", Category: "pve", Therm: 0.62, Kin: 0.38},
	{ID: "rogueDrones", Category: "pve", EM: 0.10, Therm: 0.24, Kin: 0.29, Exp: 0.37},
	{ID: "abyssTriglavian", Category: "abyss", Therm: 0.62, Exp: 0.38},
	{ID: "abyssDrifters", Category: "abyss", EM: 0.26, Therm: 0.26, Kin: 0.24, Exp: 0.24},
	{ID: "abyssSleepers", Category: "abyss", EM: 0.31, Therm: 0.31, Kin: 0.19, Exp: 0.19},
	{ID: "abyssRogueDrones", Category: "abyss", EM: 0.25, Therm: 0.25, Kin: 0.25, Exp: 0.25},
	{ID: "abyssOvermind", Category: "abyss", Therm: 0.41, Kin: 0.59},
	{ID: "abyssSansha", Category: "abyss", EM: 0.57, Therm: 0.43},
	{ID: "abyssAngel", Category: "abyss", EM: 0.45, Therm: 0.07, Kin: 0.08, Exp: 0.40},
	{ID: "abyssConcord", Category: "abyss", Therm: 0.66, Exp: 0.34},
	{ID: "abyssEdencom", Category: "abyss", EM: 0.36, Kin: 0.64},
	{ID: "pvpMultifrequency", Category: "pvp", EM: 0.58, Therm: 0.42},
	{ID: "pvpScorch", Category: "pvp", EM: 0.82, Therm: 0.18},
	{ID: "pvpAntimatter", Category: "pvp", Therm: 0.42, Kin: 0.58},
	{ID: "pvpVoid", Category: "pvp", Therm: 0.50, Kin: 0.50},
	{ID: "pvpEmp", Category: "pvp", EM: 0.75, Kin: 0.08, Exp: 0.17},
	{ID: "pvpPhasedPlasma", Category: "pvp", Therm: 0.83, Kin: 0.17},
	{ID: "pvpFusion", Category: "pvp", Kin: 0.17, Exp: 0.83},
	{ID: "pvpBarrage", Category: "pvp", Kin: 0.45, Exp: 0.55},
	{ID: "pvpBaryon", Category: "pvp", Therm: 0.60, Exp: 0.40},
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
