package gofa

import (
	"testing"

	"eve-cyno.dev/go/data/fit"
	"github.com/stretchr/testify/require"
)

// TestWeaponRange_Turret verifies that a turret module produces a RangeStats
// entry with Weapon="Turret", OptimalM from attr 54, and FalloffM from attr 158.
func TestWeaponRange_Turret(t *testing.T) {
	mod := &Item{TypeID: 1, Attrs: map[int]float64{
		attrDamageMultiplier: 2,     // marks it as a turret
		attrMaxRange:         24000, // attr 54
		attrFalloff:          12000, // attr 158
	}}
	charge := &Item{TypeID: 10, Attrs: map[int]float64{}}
	weapons := []weaponInstance{{module: mod, charge: charge}}

	got := weaponRange(weapons)

	require.Len(t, got, 1)
	require.Equal(t, fit.RangeStats{Weapon: "Turret", OptimalM: 24000, FalloffM: 12000}, got[0])
}

// TestWeaponRange_Missile verifies that a missile launcher with a charge
// produces Weapon="Missile", OptimalM = charge.Attrs[37] * charge.Attrs[281] / 1000,
// and FalloffM = 0.
func TestWeaponRange_Missile(t *testing.T) {
	mod := &Item{TypeID: 2, Attrs: map[int]float64{}} // no attr 64 → launcher
	charge := &Item{TypeID: 20, Attrs: map[int]float64{
		attrNavMaxVelocity: 3750, // attr 37: missile velocity (m/s)
		attrExplosionDelay: 8000, // attr 281: flight time (ms)
	}}
	// OptimalM = 3750 * 8000 / 1000 = 30000
	weapons := []weaponInstance{{module: mod, charge: charge}}

	got := weaponRange(weapons)

	require.Len(t, got, 1)
	require.Equal(t, fit.RangeStats{Weapon: "Missile", OptimalM: 30000, FalloffM: 0}, got[0])
}

// TestWeaponRange_Grouping verifies that two instances of the same module TypeID
// produce exactly one RangeStats entry (first-seen values).
func TestWeaponRange_Grouping(t *testing.T) {
	mod := &Item{TypeID: 1, Attrs: map[int]float64{
		attrDamageMultiplier: 2,
		attrMaxRange:         20000,
		attrFalloff:          10000,
	}}
	charge := &Item{TypeID: 10, Attrs: map[int]float64{}}
	weapons := []weaponInstance{
		{module: mod, charge: charge},
		{module: mod, charge: charge}, // duplicate TypeID
	}

	got := weaponRange(weapons)

	require.Len(t, got, 1, "two instances of the same TypeID must emit exactly one RangeStats")
}

// TestWeaponRange_Empty verifies that nil input returns nil.
func TestWeaponRange_Empty(t *testing.T) {
	got := weaponRange(nil)
	require.Nil(t, got)
}

// TestWeaponRange_UnloadedMissileLauncher verifies that a launcher with no
// charge is skipped (charge == nil).
func TestWeaponRange_UnloadedMissileLauncher(t *testing.T) {
	mod := &Item{TypeID: 3, Attrs: map[int]float64{}} // no attr 64, no charge
	weapons := []weaponInstance{{module: mod, charge: nil}}

	got := weaponRange(weapons)

	require.Nil(t, got, "unloaded launcher must be skipped")
}

// TestWeaponRange_SkipsZeroRange verifies that a module with zero optimal AND
// zero falloff is not included in the results.
func TestWeaponRange_SkipsZeroRange(t *testing.T) {
	// Turret with no range attrs set (both zero).
	mod := &Item{TypeID: 4, Attrs: map[int]float64{
		attrDamageMultiplier: 2, // is a turret, but no range data
	}}
	charge := &Item{TypeID: 40, Attrs: map[int]float64{}}
	weapons := []weaponInstance{{module: mod, charge: charge}}

	got := weaponRange(weapons)

	require.Nil(t, got, "module with OptimalM=0 and FalloffM=0 must be skipped")
}
