package gofa

import (
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit"
)

// M4 (fit parity, owner decision 2026-11-10): a skill the hull or a fitted subsystem requires
// and the pilot does not have at all counts as level I, like the game's fitting simulation.
// A trained level below the requirement is not raised; module skills are not raised.
// Callers: only a profile with MissingAsZero (the desktop's real characters). Web and every
// all-V caller leave it unset and are unchanged.

const (
	skillGallenteBattleship  = 3336
	skillGallenteBattlecr    = 33097
	skillCaldariCoreSystems  = 30548
	skillSmallProjectileTurr = 3302
)

// profileOf is a real-character profile: the listed levels and nothing else.
func profileOf(levels map[int]int) fit.StatsOpts {
	return fit.StatsOpts{Skills: fit.SkillProfile(levels), MissingAsZero: true, AbsentHullSkillAtI: true}
}

// Tengu: Caldari Core Systems gives 5 % capacitor capacity per level (subsystem effect).
func TestHullSkill_SubsystemSkillAbsentCountsOne(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 4)
	absent := statsOf(t, eng, f, profileOf(map[int]int{})).Capacitor.Capacity
	zero := statsOf(t, eng, f, profileOf(map[int]int{skillCaldariCoreSystems: 0})).Capacitor.Capacity
	one := statsOf(t, eng, f, profileOf(map[int]int{skillCaldariCoreSystems: 1})).Capacitor.Capacity
	require.InDelta(t, one, absent, 1e-9, "absent counts as I")
	require.InDelta(t, 1.05, absent/zero, 1e-6, "a skill listed at 0 stays 0")
}

// Megathron: Gallente Battleship gives 5 % large hybrid rate of fire per level.
func TestHullSkill_HullSkillAbsentCountsOne(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 5)
	dps := func(l map[int]int) float64 { return statsOf(t, eng, f, profileOf(l)).DPS.Theoretical }
	absent, zero, one := dps(map[int]int{}), dps(map[int]int{skillGallenteBattleship: 0}), dps(map[int]int{skillGallenteBattleship: 1})
	require.InDelta(t, one, absent, 1e-6)
	require.Greater(t, absent, zero)
}

// Myrmidon: Gallente Battlecruiser gives 10 % drone damage per level.
func TestHullSkill_DroneBonusHullSkill(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 6)
	drones := func(l map[int]int) float64 { return statsOf(t, eng, f, profileOf(l)).Drones.DPS }
	absent, zero := drones(map[int]int{skillDrones: 5}), drones(map[int]int{skillDrones: 5, skillGallenteBattlecr: 0})
	require.InDelta(t, 1.10, absent/zero, 1e-6)
}

// A trained level below the requirement is not raised; a module skill that is absent stays 0.
func TestHullSkill_NotRaisedElsewhere(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 3)
	three := statsOf(t, eng, f, profileOf(map[int]int{skillCaldariCruiser: 3})).Volley
	two := statsOf(t, eng, f, profileOf(map[int]int{skillCaldariCruiser: 2})).Volley
	require.Greater(t, three, two, "Caldari Cruiser 2 and 3 differ: nothing raises a trained level")

	rifter := parityFit(t, s, 1)
	absent := statsOf(t, eng, rifter, profileOf(map[int]int{})).DPS.Theoretical
	zero := statsOf(t, eng, rifter, profileOf(map[int]int{skillSmallProjectileTurr: 0})).DPS.Theoretical
	require.InDelta(t, zero, absent, 1e-9, "Small Projectile Turret absent is 0, not I")
}

// The default model is unchanged: no profile means all V; a profile without MissingAsZero keeps V.
func TestHullSkill_OtherCallersUnchanged(t *testing.T) {
	s := openRealSDE(t)
	defer s.Close()
	eng := New(s)
	f := parityFit(t, s, 5)
	allV := statsOf(t, eng, f, fit.StatsOpts{}).DPS.Theoretical
	partial := statsOf(t, eng, f, fit.StatsOpts{Skills: fit.SkillProfile{skillCaldariCruiser: 3}}).DPS.Theoretical
	require.InDelta(t, allV, partial, 1e-9)
}
