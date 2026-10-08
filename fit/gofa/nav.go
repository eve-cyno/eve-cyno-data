package gofa

import (
	"math"

	"eve-cyno.dev/go/data/fit"
)

// Dogma attribute IDs used by the navigation calculator.
const (
	attrNavMaxVelocity = 37  // maximum velocity (m/s)
	attrNavMass        = 4   // ship mass (kg)
	attrNavAgility     = 70  // inertiaModifier (dimensionless)
	attrNavSigRadius   = 552 // signatureRadius (m) — also declared in engine.go as attrSignatureRadius
)

// navigation computes NavStats from the resolved ship Item.
//
// Formulas:
//   - MaxVelocity    = ship.Attrs[37]                             (m/s, already resolved)
//   - SignatureRadius = ship.Attrs[552]                           (m, already resolved)
//   - AlignTimeSec   = -ln(0.25) * mass * agility / 1_000_000    (seconds)
//
// Align time is the time a ship needs to reach 75% of its maximum velocity when
// its speed approaches that maximum exponentially with time constant
// mass × inertiaModifier / 1 000 000, so the multiplier is -ln(0.25) = ln(4) =
// 2*ln(2), not ln(2):
//
//	alignTime = -ln(0.25) × mass × inertiaModifier / 1 000 000
//
// inertiaModifier is SDE dogma attribute 70 and mass is attribute 4. The
// /1_000_000 converts from (kg × dimensionless) to seconds: EVE stores mass in
// kg and inertiaModifier is a pure multiplier. Resulting values are
// cross-checked against Pyfa v2.67.0 output (validation reference only; see testdata/golden/gofa/README.md).
func navigation(ship *Item) fit.NavStats {
	vel := ship.Attrs[attrNavMaxVelocity]
	sig := ship.Attrs[attrNavSigRadius]
	mass := ship.Attrs[attrNavMass]
	agility := ship.Attrs[attrNavAgility]

	// -ln(0.25) = ln(4) = 2·ln(2) ≈ 1.38629
	alignTime := -math.Log(0.25) * mass * agility / 1_000_000

	return fit.NavStats{
		MaxVelocity:     vel,
		AlignTimeSec:    alignTime,
		SignatureRadius: sig,
	}
}
