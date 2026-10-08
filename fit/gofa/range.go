package gofa

import "eve-cyno.dev/go/data/fit"

// Dogma attribute IDs used by the range calculator.
const (
	attrMaxRange       = 54  // maxRange — turret optimal range (m)
	attrFalloff        = 158 // falloff — turret falloff range (m)
	attrExplosionDelay = 281 // explosionDelay — missile flight time (ms)
	// attrMissileVelocity reuses attrNavMaxVelocity (= 37) declared in nav.go;
	// attr 37 is missile velocity on charge items as well as ship max velocity.
)

// weaponRange computes per-weapon-group range statistics from a slice of
// resolved weaponInstances. It emits one fit.RangeStats per distinct module
// TypeID (first-seen order):
//
//   - Turret (isTurret returns true): OptimalM = module.Attrs[54],
//     FalloffM = module.Attrs[158], Weapon = "Turret".
//   - Missile launcher (not turret): requires a loaded charge; if charge is nil
//     the weapon is skipped. OptimalM = charge.Attrs[37] × charge.Attrs[281] / 1000,
//     FalloffM = 0, Weapon = "Missile".
//
// Entries where both OptimalM and FalloffM are zero (e.g. unranged smartbombs
// or turrets with no range dogma data) are omitted. Returns nil for empty input.
func weaponRange(weapons []weaponInstance) []fit.RangeStats {
	if len(weapons) == 0 {
		return nil
	}

	seen := make(map[int]bool)
	var result []fit.RangeStats

	for _, w := range weapons {
		typeID := w.module.TypeID
		if seen[typeID] {
			continue
		}

		var rs fit.RangeStats
		if isTurret(w.module) {
			rs = fit.RangeStats{
				Weapon:   "Turret",
				OptimalM: w.module.Attrs[attrMaxRange],
				FalloffM: w.module.Attrs[attrFalloff],
			}
		} else {
			// Missile launcher — skip if no charge loaded.
			if w.charge == nil {
				continue
			}
			flightTimeSec := w.charge.Attrs[attrExplosionDelay] / 1000.0
			rs = fit.RangeStats{
				Weapon:   "Missile",
				OptimalM: w.charge.Attrs[attrNavMaxVelocity] * flightTimeSec,
				FalloffM: 0,
			}
		}

		// Skip modules that yield no meaningful range.
		if rs.OptimalM <= 0 && rs.FalloffM <= 0 {
			continue
		}

		seen[typeID] = true
		result = append(result, rs)
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
