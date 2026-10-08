package gofa

import (
	"eve-cyno.dev/go/data/fit"
)

// Damage dogma attribute IDs (on charges and drones).
const (
	attrEMDamage         = 114
	attrExplosiveDamage  = 116
	attrKineticDamage    = 117
	attrThermalDamage    = 118
	attrDamageMultiplier = 64
	attrSpeed            = 51 // weapon cycle time (ms); also launcher RoF
)

// weaponInstance pairs a resolved weapon module Item with its loaded charge Item
// (nil when no charge is fitted). Drones are handled separately.
type weaponInstance struct {
	module *Item
	charge *Item // nil for bare modules (drones handled via drones slice)
}

// isTurret reports whether a weapon module is a turret: it must have a positive
// damageMultiplier attribute (attr 64). Launchers do NOT have this attribute.
func isTurret(mod *Item) bool {
	return mod.Attrs[attrDamageMultiplier] > 0
}

// chargeDamage sums the four damage type attributes on an Item (charge or drone).
func chargeDamage(it *Item) (em, exp, kin, therm float64) {
	return it.Attrs[attrEMDamage],
		it.Attrs[attrExplosiveDamage],
		it.Attrs[attrKineticDamage],
		it.Attrs[attrThermalDamage]
}

// computeDPS calculates theoretical DPS and volley for all weapon instances and
// drones, populating fit.DPSStats and the total volley value.
//
// Theoretical DPS (full application, reload time not factored in; values
// cross-checked against Pyfa v2.67.0 output):
//
//	TURRET (module has damageMultiplier attr 64 AND a loaded charge):
//	  volley  = (em+exp+kin+therm from charge) × module.damageMultiplier
//	  dps     = volley / (module.speed / 1000)
//
//	MISSILE (launcher: no damageMultiplier, has a loaded missile charge):
//	  volley  = sum of resolved em+exp+kin+therm on the charge
//	  dps     = volley / (launcher.speed / 1000)
//	  Note: missile damage attrs ARE already resolved (BCS/skill bonuses applied
//	  to the charge Item by LocationRequiredSkillModifier during resolve).
//
//	DRONE:
//	  volley  = (em+exp+kin+therm from drone) × drone.damageMultiplier
//	  dps     = volley / (drone.speed / 1000)
//	  Total   = sum across all drone instances (already expanded per-unit in resolve).
//
// label, when non-nil, names each PerWeapon entry from the module and charge
// typeIDs; nil keeps the generic "weapon" label.
func computeDPS(weapons []weaponInstance, drones []*Item, label func(module, charge int) string) (stats fit.DPSStats, totalVolley float64, droneDPS float64) {
	var weaponDPS, weaponVolley, droneVolley float64

	for _, w := range weapons {
		if w.charge == nil {
			continue // no charge loaded — weapon contributes zero DPS
		}
		cycleMS := w.module.Attrs[attrSpeed]
		if cycleMS <= 0 {
			continue
		}

		em, exp, kin, therm := chargeDamage(w.charge)

		var volley float64
		if isTurret(w.module) {
			// Turret: charge base damage × turret's resolved damageMultiplier.
			volley = (em + exp + kin + therm) * w.module.Attrs[attrDamageMultiplier]
		} else {
			// Missile launcher: damage is the missile charge's RESOLVED attrs
			// (BCS / skills applied to the charge during resolveModifiers via
			// LocationRequiredSkillModifier). No per-launcher multiplier.
			volley = em + exp + kin + therm
		}

		dps := volley / (cycleMS / 1000.0)
		weaponDPS += dps
		weaponVolley += volley
		name := "weapon"
		if label != nil {
			name = label(w.module.TypeID, w.charge.TypeID)
		}
		stats.PerWeapon = append(stats.PerWeapon, fit.WeaponDPS{
			Weapon:      name,
			Theoretical: dps,
		})
	}

	for _, d := range drones {
		cycleMS := d.Attrs[attrSpeed]
		if cycleMS <= 0 {
			continue
		}
		em, exp, kin, therm := chargeDamage(d)
		// Drone damage: base damage × drone's resolved damageMultiplier.
		volley := (em + exp + kin + therm) * d.Attrs[attrDamageMultiplier]
		dps := volley / (cycleMS / 1000.0)
		droneDPS += dps
		droneVolley += volley
	}

	stats.Theoretical = weaponDPS + droneDPS
	totalVolley = weaponVolley + droneVolley
	return stats, totalVolley, droneDPS
}
