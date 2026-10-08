package gofa

import "eve-cyno.dev/go/data/fit"

// Dogma attribute IDs used by the EHP / tank calculator.
const (
	// Raw HP attributes (on the ship hull).
	attrShieldHP = 263
	attrArmorHP  = 265
	attrHullHP   = 9

	// Shield resonances (post-resist multipliers: resonance = 1 − resist).
	attrShieldResEM    = 271
	attrShieldResTherm = 272
	attrShieldResKin   = 273
	attrShieldResExp   = 274

	// Armor resonances.
	attrArmorResEM    = 267
	attrArmorResTherm = 268
	attrArmorResKin   = 269
	attrArmorResExp   = 270

	// Hull (structure) resonances — from dgmAttributeTypes in the EVE SDE.
	// These are the *structure* resonance attributes (not armor/shield):
	//   emDamageResonance=113, thermalDamageResonance=110,
	//   kineticDamageResonance=109, explosiveDamageResonance=111.
	// Note: the task spec listed 113/114/111/109 but SDE confirms 113/110/109/111.
	attrHullResEM    = 113
	attrHullResTherm = 110
	attrHullResKin   = 109
	attrHullResExp   = 111

	// Active-rep attributes (on modules).
	attrShieldBoostAmount = 68 // shieldBonus on shield boosters
	attrArmorRepairAmount = 84 // armorDamageAmount on armor repairers
	attrModuleDuration    = 73 // duration (ms) — used for rep and cap-usage rate
)

// resonances returns the four resonance values (EM, Therm, Kin, Exp) for a
// layer from the ship's resolved attributes. If an attribute is absent the
// resonance falls back to 1.0 (no resistance).
func resonances(attrs map[int]float64, emID, thermID, kinID, expID int) (em, therm, kin, exp float64) {
	val := func(id int) float64 {
		if v, ok := attrs[id]; ok {
			return v
		}
		return 1.0
	}
	return val(emID), val(thermID), val(kinID), val(expID)
}

// layerEHP computes the effective HP of one tank layer using the uniform damage
// pattern (25/25/25/25), the same pattern the oracle reference values use (Pyfa
// run with no damage pattern set).
//
// EHP = HP / mean(resonance_EM, resonance_Therm, resonance_Kin, resonance_Exp)
//
// A resonance of 1.0 means no resistance (full damage passes through).
// A resonance of 0.0 means perfect resistance — we guard against division by
// zero by returning HP when mean resonance would be ≤ 0.
func layerEHP(hp, emRes, thermRes, kinRes, expRes float64) float64 {
	mean := (emRes + thermRes + kinRes + expRes) / 4.0
	if mean <= 0 {
		return hp // degenerate: treat as infinite EHP
	}
	return hp / mean
}

// resistProfile converts four resonance values into a ResistProfile.
// resist = 1 − resonance (resistance is the fraction of damage blocked).
func resistProfile(em, therm, kin, exp float64) fit.ResistProfile {
	return fit.ResistProfile{
		EM:    1 - em,
		Therm: 1 - therm,
		Kin:   1 - kin,
		Exp:   1 - exp,
	}
}

// tank computes TankStats from the resolved ship Item and fitted modules.
//
// EHP formula (uniform 25/25/25/25 damage profile):
//
//	EHP_layer = HP / mean(resonance_EM, resonance_Therm, resonance_Kin, resonance_Exp)
//
// Resonance values are read directly from the resolved ship attributes.
// Active rep per second is the sum over modules of
// (boost/repair amount) / (duration_ms / 1000).
func tank(ship *Item, items []*Item) fit.TankStats {
	// Layer HP.
	shieldHP := ship.Attrs[attrShieldHP]
	armorHP := ship.Attrs[attrArmorHP]
	hullHP := ship.Attrs[attrHullHP]

	// Shield resonances.
	sEM, sTherm, sKin, sExp := resonances(ship.Attrs,
		attrShieldResEM, attrShieldResTherm, attrShieldResKin, attrShieldResExp)

	// Armor resonances.
	aEM, aTherm, aKin, aExp := resonances(ship.Attrs,
		attrArmorResEM, attrArmorResTherm, attrArmorResKin, attrArmorResExp)

	// Hull resonances.
	hEM, hTherm, hKin, hExp := resonances(ship.Attrs,
		attrHullResEM, attrHullResTherm, attrHullResKin, attrHullResExp)

	// EHP per layer.
	shieldEHP := layerEHP(shieldHP, sEM, sTherm, sKin, sExp)
	armorEHP := layerEHP(armorHP, aEM, aTherm, aKin, aExp)
	hullEHP := layerEHP(hullHP, hEM, hTherm, hKin, hExp)

	// Active rep per second: sum over modules where the module has either
	// a shield boost amount or an armor repair amount AND a duration > 0.
	var activeRepPerSec float64
	for _, it := range items {
		if it.Kind != KindModule {
			continue
		}
		dur := it.Attrs[attrModuleDuration] // ms
		if dur <= 0 {
			continue
		}
		durSec := dur / 1000.0

		if boost := it.Attrs[attrShieldBoostAmount]; boost > 0 {
			activeRepPerSec += boost / durSec
		}
		if rep := it.Attrs[attrArmorRepairAmount]; rep > 0 {
			activeRepPerSec += rep / durSec
		}
	}

	return fit.TankStats{
		ShieldHP:        shieldHP,
		ArmorHP:         armorHP,
		HullHP:          hullHP,
		ShieldResists:   resistProfile(sEM, sTherm, sKin, sExp),
		ArmorResists:    resistProfile(aEM, aTherm, aKin, aExp),
		HullResists:     resistProfile(hEM, hTherm, hKin, hExp),
		TotalEHP:        shieldEHP + armorEHP + hullEHP,
		ActiveRepPerSec: activeRepPerSec,
	}
}
