package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// Unit tests for the QC2 off-bonus-weapon ranking (QC2 analysis
// §7, eval Q142): hull weapon affinity from SDE trait texts, module / fit weapon
// classification, the penalty and its wiring through SearchFits.

// Trait texts below are verbatim invTraits.bonusText rows of the SDE (anchors
// stripped, as sde.GetShipTraits returns them).
func TestWeaponSystemsFromTraits(t *testing.T) {
	tests := []struct {
		name  string
		texts []string
		want  []WeaponSystem
	}{
		{"Punisher — laser by activation cost", []string{
			"reduction in Small Energy Turret activation cost", "bonus to all armor resistances"},
			[]WeaponSystem{SystemEnergy}},
		{"Retribution", []string{
			"bonus to Small Energy Turret rate of fire", "reduction in Small Energy Turret activation cost",
			"bonus to Small Energy Turret damage", "bonus to Small Energy Turret optimal range",
			"Can fit Assault Damage Controls", "reduction in Microwarpdrive signature radius penalty"},
			[]WeaponSystem{SystemEnergy}},
		{"Stabber — projectile", []string{
			"bonus to Medium Projectile Turret rate of fire", "bonus to Medium Projectile Turret falloff"},
			[]WeaponSystem{SystemProjectile}},
		{"Vexor — hybrid + drones", []string{
			"bonus to Medium Hybrid Turret damage", "bonus to Drone hitpoints, damage and mining yield"},
			[]WeaponSystem{SystemHybrid, SystemDrone}},
		{"Tristan — hybrid + drones", []string{
			"bonus to Small Hybrid Turret tracking speed", "bonus to Drone hitpoints and tracking speed"},
			[]WeaponSystem{SystemHybrid, SystemDrone}},
		{"Caracal — missiles", []string{
			"bonus to Rapid Light Missile, Heavy Missile and Heavy Assault Missile Launcher rate of fire",
			"bonus to Heavy Missile and Heavy Assault Missile max velocity"},
			[]WeaponSystem{SystemMissile}},
		{"Drake — missiles (no weapon skill link)", []string{
			"Can use one Command Burst module", "bonus to Missile velocity",
			"bonus to Command Burst area of effect range", "bonus to all shield resistances",
			"bonus to kinetic Heavy Missile and Heavy Assault Missile damage"},
			[]WeaponSystem{SystemMissile}},
		{"Ishtar — drones only", []string{
			"Can fit Assault Damage Controls", "reduction in Microwarpdrive signature radius penalty",
			"bonus to Sentry Drone hitpoints and damage", "bonus to Heavy Drone max velocity and tracking speed",
			"bonus to Drone operation range"},
			[]WeaponSystem{SystemDrone}},
		{"Dominix — drones only since the hybrid bonus left it", []string{
			"additional bonus to Reinforced Bulkhead hitpoints", "bonus to Armor Plate hitpoints",
			"bonus to Shield Extender hitpoints", "bonus to Drone optimal range and tracking speed",
			"bonus to Drone hitpoints and damage"},
			[]WeaponSystem{SystemDrone}},
		{"Myrmidon — drones only (command burst and repairer bonuses name no weapon)", []string{
			"Can use one Command Burst module", "bonus to Drone microwarp velocity",
			"bonus to Command Burst area of effect range", "bonus to Armor Repairer amount",
			"bonus to Drone hitpoints and damage"},
			[]WeaponSystem{SystemDrone}},
		{"Armageddon — drones only, the neutralizer range bonus names no weapon", []string{
			"bonus to Shield Extender hitpoints", "bonus to Armor Plate hitpoints",
			"additional bonus to Reinforced Bulkhead hitpoints", "bonus to Drone hitpoints and damage",
			"bonus to Energy Nosferatu and Energy Neutralizer optimal range",
			"bonus to Energy Nosferatu and Energy Neutralizer falloff range"},
			[]WeaponSystem{SystemDrone}},
		{"Rifter — projectile", []string{
			"bonus to Small Projectile Turret rate of fire", "bonus to Small Projectile Turret falloff"},
			[]WeaponSystem{SystemProjectile}},
		{"Gila — generic missile text + drones", []string{
			"bonus to Medium Combat Drone hitpoints", "bonus to Medium Combat Drone damage",
			"bonus to kinetic and thermal missile damage", "bonus to all shield resistances"},
			[]WeaponSystem{SystemMissile, SystemDrone}},
		{"Hawk — rockets", []string{
			"bonus to Light Missile and Rocket damage", "bonus to Shield Booster amount"},
			[]WeaponSystem{SystemMissile}},
		{"Nemesis — torpedoes, bomb launcher is no weapon system", []string{
			"Can fit Covert Ops Cloaking Device, Covert Cynosural Field Generator and Bomb Launcher",
			"bonus to Torpedo max velocity"},
			[]WeaponSystem{SystemMissile}},
		{"Praxis — every turret system, missiles and drones", []string{
			"bonus to Large Hybrid Turret, Large Energy Turret and Large Projectile Turret damage",
			"bonus to Heavy Missile, Cruise Missile and Torpedo damage", "bonus to Drone hitpoints and damage"},
			[]WeaponSystem{SystemEnergy, SystemHybrid, SystemProjectile, SystemMissile, SystemDrone}},
		{"Damavik — disintegrators; neut and smartbomb texts name no system", []string{
			"bonus to Light Entropic Disintegrator damage", "reduced Energy Neutralizer capacitor need",
			"reduced Smart Bomb capacitor need"},
			[]WeaponSystem{SystemPrecursor}},
		{"Thunderchild — vorton projectors", []string{"bonus to Large Vorton Projector damage"},
			[]WeaponSystem{SystemPrecursor}},
		{"fitting-requirement bonus counts (Oracle)", []string{
			"reduction in Large Energy Turret powergrid requirement"},
			[]WeaponSystem{SystemEnergy}},
		{"Garmur — a drawback names no system on its own", []string{"penalty to missile flight time"}, nil},
		{"Nosferatu / neutralizer hull", []string{
			"bonus to Energy Nosferatu and Energy Neutralizer drain amount"}, nil},
		{"no weapon bonus (Command Burst hull)", []string{"bonus to Command Burst area of effect range"}, nil},
		{"empty", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, WeaponSystemsFromTraits(tc.texts))
		})
	}
}

// Module names are real EFT names from the prod corpus (incl. meta variants).
func TestModuleWeaponSystem(t *testing.T) {
	cases := map[WeaponSystem][]string{
		SystemEnergy: {
			"Small Focused Pulse Laser II", "Heavy Beam Laser II", "Dual Giga Pulse Laser II", "Tachyon Beam Laser II",
			"Gatling Pulse Laser II", "Mega Modulated Pulse Energy Beam I", "Small Focused Anode Particle Stream I",
			"Heavy Afocal Laser I", "Quad Modal Light Laser I",
		},
		SystemHybrid: {
			"Light Neutron Blaster II", "250mm Railgun II", "75mm Gatling Rail I", "Neutron Blaster Cannon II",
			"Anode Light Neutron Particle Cannon I", "150mm 'Scout' Accelerator Cannon", "425mm Prototype Gauss Gun",
			"Modal Mega Neutron Particle Accelerator I", "Regulated Light Neutron Phase Cannon I",
			"250mm Compressed Coil Gun I", "Polarized Light Neutron Blaster",
		},
		SystemProjectile: {
			"200mm AutoCannon II", "125mm Gatling AutoCannon II", "Dual 180mm AutoCannon II", "800mm Repeating Cannon II",
			"280mm Howitzer Artillery II", "1400mm 'Scout' Artillery I", "250mm Light Artillery Cannon II",
			"200mm Light Prototype Automatic Cannon", "150mm Light 'Scout' Autocannon I", "250mm Light Gallium Cannon",
			"125mm Light Gallium Machine Gun", "1400mm Prototype Siege Cannon",
		},
		SystemMissile: {
			"Rapid Light Missile Launcher II", "Rocket Launcher II", "Heavy Assault Missile Launcher II",
			"Torpedo Launcher II", "Cruise Missile Launcher II", "'Arbalest' Cruise Launcher I",
			"XR-3200 Heavy Missile Bay", "Caldari Navy Rocket Launcher", "Polarized Torpedo Launcher",
		},
		SystemPrecursor: {
			"Light Entropic Disintegrator II", "Veles Heavy Entropic Disintegrator", "Medium Vorton Projector II",
			"Large Consortium Vorton Projector",
		},
		"": { // not turrets / launchers — must never classify
			"Mining Laser Upgrade II", "Ice Mining Laser II", "EP-S Gaussian Scoped Mining Laser",
			"Mining Foreman Link - Laser Optimization II", "Core Probe Launcher I", "Sisters Expanded Probe Launcher",
			"Interdiction Sphere Launcher I", "Bomb Launcher I", "Defender Launcher I", "Festival Launcher",
			"Small Breacher Pod Launcher", "Vorton Tuning System II", "Warp Scrambler II", "Stasis Webifier II",
			"Small Armor Repairer II", "Heavy Energy Neutralizer II", "Medium Compact Pb-Acid Cap Battery",
			"Hobgoblin II", "Scourge Heavy Missile", "Multifrequency M", "Antimatter Charge M", "Small EMP Smartbomb I",
			"Author: Anonymous | 3 hours ago | Tags:",
		},
	}
	for want, names := range cases {
		for _, name := range names {
			require.Equal(t, want, moduleWeaponSystem(name), "moduleWeaponSystem(%q)", name)
		}
	}
}

const punisherAutocannonEFT = `# Punisher — T1 Brick-Tank Bait Punisher (★ Quality 0.22)
Source: https://eveworkbench.com/fit/x
Author: Anonymous | 2 hours ago | Tags: Blaster, Disintegrator | Tag search: pvp
Stats: DPS 0 | EHP 0.0k | Rep 100 ehp/s | Speed 1059 m/s | Cost 21M ISK
0 views | tested: false | video: false | alpha: true

[Punisher, T1 Brick-Tank Bait Punisher]
150mm Light AutoCannon II, Republic Fleet Phased Plasma S
150mm Light AutoCannon II, Republic Fleet Phased Plasma S
150mm Light AutoCannon II
150mm Light AutoCannon II

Warp Scrambler II
1MN Afterburner II

Small Armor Repairer II
Damage Control II

Small Core Defense Field Extender I

Hobgoblin II x5
Republic Fleet Phased Plasma S x1000`

// ishtarLaserEFT is the fit eval Q82 ("most tested Ishtar build for firestorm
// abyss") returned: four small beam lasers on the drone-bonused Ishtar, the
// highest stored score (0.54) of the 60 Ishtar fits of the 2026-10-06 corpus.
const ishtarLaserEFT = `[Ishtar, 이머징이머징????]
Imperial Navy Small Focused Beam Laser
Imperial Navy Small Focused Beam Laser
Imperial Navy Small Focused Beam Laser
Imperial Navy Small Focused Beam Laser

Pithum B-Type Multispectrum Shield Hardener
Domination Large Cap Battery
Gistum A-Type 10MN Afterburner
Gist A-Type X-Large Shield Booster

Shadow Serpentis Assault Damage Control
Sentient Omnidirectional Tracking Enhancer
Dread Guristas Drone Damage Amplifier
Dread Guristas Drone Damage Amplifier
Dread Guristas Drone Damage Amplifier
Dark Blood Power Diagnostic System

Medium EM Shield Reinforcer II
Medium Semiconductor Memory Cell II`

// ishtarDroneEFT is a drone-boat Ishtar: utility highs (neutralizer, nosferatu,
// probe launcher, cloak), drone-damage lows and a drone bay.
const ishtarDroneEFT = `[Ishtar, drone boat]
Small Energy Neutralizer II
Small Energy Nosferatu II
Core Probe Launcher I
Covert Ops Cloaking Device II

Large Shield Extender II
10MN Afterburner II

Drone Damage Amplifier II
Drone Damage Amplifier II
Drone Damage Amplifier II
Damage Control II

Medium Core Defense Field Extender I

Ogre II x2
Hammerhead II x2`

// ishtarLoneTurretEFT carries ONE small turret among tractor / cloak utility highs
// — a filler weapon, not a weapon fit (all 11 single-weapon fits of the pure
// drone hulls in the corpus look like this).
const ishtarLoneTurretEFT = `[Ishtar, explorer]
Imperial Navy Small Focused Beam Laser
Small Tractor Beam I
Covert Ops Cloaking Device II
Sisters Core Probe Launcher

Large Shield Extender II
10MN Afterburner II

Drone Damage Amplifier II
Drone Damage Amplifier II`

func TestFitWeaponCounts(t *testing.T) {
	t.Run("metadata preamble is not read as modules", func(t *testing.T) {
		// "Tags: Blaster, Disintegrator" sits ABOVE the [Hull, Fit] header.
		require.Equal(t, map[WeaponSystem]int{SystemProjectile: 4}, fitWeaponCounts(punisherAutocannonEFT))
	})
	t.Run("charges and offline markers are cut off, bracketed fit names parse", func(t *testing.T) {
		eft := "[Punisher, [HF] Laser /w point]\nHeavy Pulse Laser II, Multifrequency M\nHeavy Pulse Laser II /OFFLINE\n[Empty High slot]\n\nWarp Scrambler II"
		require.Equal(t, map[WeaponSystem]int{SystemEnergy: 2}, fitWeaponCounts(eft))
	})
	t.Run("mixed systems are counted separately", func(t *testing.T) {
		eft := "[Stabber, x]\n200mm AutoCannon II\n200mm AutoCannon II\nRocket Launcher II\nCore Probe Launcher I"
		require.Equal(t, map[WeaponSystem]int{SystemProjectile: 2, SystemMissile: 1}, fitWeaponCounts(eft))
	})
	t.Run("text without an EFT header yields nothing", func(t *testing.T) {
		require.Nil(t, fitWeaponCounts("Heavy Pulse Laser II\nWarp Scrambler II"))
		require.Nil(t, fitWeaponCounts(""))
	})
	t.Run("no turrets or launchers", func(t *testing.T) {
		require.Empty(t, fitWeaponCounts("[Punisher, salvager]\nSalvager I\nSalvager I\n\n1MN Afterburner II"))
	})
}

func TestMainWeaponSystems(t *testing.T) {
	require.Nil(t, mainWeaponSystems(nil))
	require.Equal(t, []WeaponSystem{SystemEnergy}, mainWeaponSystems(map[WeaponSystem]int{SystemEnergy: 3, SystemMissile: 1}))
	require.Equal(t, []WeaponSystem{SystemEnergy, SystemProjectile},
		mainWeaponSystems(map[WeaponSystem]int{SystemEnergy: 2, SystemProjectile: 2, SystemMissile: 1}))
}

func TestIsOffBonus(t *testing.T) {
	lasers := "[Punisher, l]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\nWarp Scrambler II"
	mixedLaserMajority := "[Punisher, m]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\n150mm Light AutoCannon II"
	tie := "[Punisher, t]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\n150mm Light AutoCannon II\n150mm Light AutoCannon II"
	salvager := "[Punisher, s]\nSalvager I\nSalvager I\n1MN Afterburner II"
	punisher := []WeaponSystem{SystemEnergy}

	tests := []struct {
		name string
		eft  string
		hull []WeaponSystem
		want bool
	}{
		{"autocannons on a laser hull (Q142)", punisherAutocannonEFT, punisher, true},
		{"lasers on a laser hull", lasers, punisher, false},
		{"laser majority with one autocannon", mixedLaserMajority, punisher, false},
		{"exact tie counts as on-bonus when one tied system is bonused", tie, punisher, false},
		{"no turrets or launchers (salvager) is not penalised", salvager, punisher, false},
		{"text without a fit header is not penalised", "Small Focused Pulse Laser II", punisher, false},
		{"unknown hull (nil lookup result)", punisherAutocannonEFT, nil, false},
		{"hull without a weapon bonus", punisherAutocannonEFT, []WeaponSystem{}, false},
		{"hybrid + drone hull (Vexor) with lasers is a legitimate drone boat", lasers, []WeaponSystem{SystemHybrid, SystemDrone}, false},
		{"missile + drone hull (Gila) with autocannons is exempt", punisherAutocannonEFT, []WeaponSystem{SystemMissile, SystemDrone}, false},
		{"hybrid hull with lasers (Atron 'FW-4x4')", lasers, []WeaponSystem{SystemHybrid}, true},
		{"multi-system hull bonus (Praxis-style) accepts any bonused system", punisherAutocannonEFT,
			[]WeaponSystem{SystemEnergy, SystemHybrid, SystemProjectile}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOffBonus(tc.eft, HullAffinity{Bonus: tc.hull}))
			// A racial doctrine is only ever read for drone-only hulls: it changes
			// nothing for any other hull.
			require.Equal(t, tc.want, isOffBonus(tc.eft, HullAffinity{Bonus: tc.hull, Racial: []WeaponSystem{SystemProjectile}}))
		})
	}
}

// Q82 rule: on a drone-only hull a fit is off-bonus when it carries
// droneHullMinWeapons or more turrets / launchers of a system the hull's race does
// not field. Racial filler (lasers on an Amarr drone hull, blasters on a Gallente
// one) is legitimate meta and is never penalised.
func TestIsOffBonus_droneHull(t *testing.T) {
	var (
		drone    = []WeaponSystem{SystemDrone}
		amarr    = []WeaponSystem{SystemEnergy}
		gallente = []WeaponSystem{SystemHybrid}
		caldari  = []WeaponSystem{SystemMissile, SystemHybrid}
		minmatar = []WeaponSystem{SystemProjectile}
	)
	dragoonPulse := "[Dragoon, pulse]\n" + strings.Repeat("Small Focused Pulse Laser II\n", 4) + "Small Energy Neutralizer II\n\nDrone Damage Amplifier II"
	dominixBlasters := "[Dominix, blasters]\n" + strings.Repeat("Mega Neutron Blaster Cannon II\n", 4) + "\nDrone Damage Amplifier II"
	dominixLasers := "[Dominix, lasers]\n" + strings.Repeat("Mega Pulse Laser II\n", 4) + "\nDrone Damage Amplifier II"
	oneOffTwoOn := "[Ishtar, m]\nSmall Focused Beam Laser II\n150mm Railgun II\n150mm Railgun II\nSmall Tractor Beam I"
	twoOffTwoOn := "[Ishtar, m]\nSmall Focused Beam Laser II\nSmall Focused Beam Laser II\n150mm Railgun II\n150mm Railgun II"
	missilesAndRails := "[Rattlesnake, m]\n" + strings.Repeat("Heavy Missile Launcher II\n", 3) + strings.Repeat("150mm Railgun II\n", 3)

	tests := []struct {
		name   string
		eft    string
		racial []WeaponSystem
		want   bool
	}{
		{"Ishtar (Gallente) with four small beam lasers — the Q82 fit", ishtarLaserEFT, gallente, true},
		{"Ishtar with autocannons", punisherAutocannonEFT, gallente, true},
		{"Ishtar with two lasers is already an off-race loadout", "[Ishtar, l]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II", gallente, true},
		{"Ishtar with missiles", "[Ishtar, m]\nRocket Launcher II\nRocket Launcher II", gallente, true},
		{"Ishtar with a single filler turret among utility highs", ishtarLoneTurretEFT, gallente, false},
		{"Ishtar drone boat with neut / nos / probe / cloak highs", ishtarDroneEFT, gallente, false},
		{"Ishtar with no turrets or launchers", "[Ishtar, s]\nSalvager I\nSalvager I\n1MN Afterburner II", gallente, false},
		{"Ishtar, text without a fit header", "Small Focused Pulse Laser II\nSmall Focused Pulse Laser II", gallente, false},
		{"Dragoon (Amarr) with pulse lasers is racial filler", dragoonPulse, amarr, false},
		{"Dominix (Gallente) with large blasters is racial filler", dominixBlasters, gallente, false},
		{"Dominix with lasers is off-race", dominixLasers, gallente, true},
		{"Dragoon with blasters is off-race", dominixBlasters, amarr, true},
		{"Amarr hull with missiles is off-race", "[Armageddon, m]\nHeavy Missile Launcher II\nHeavy Missile Launcher II", amarr, true},
		{"Minmatar hull with autocannons is racial filler", punisherAutocannonEFT, minmatar, false},
		{"Caldari hull with missiles is racial filler", "[Scorpion, m]\nHeavy Missile Launcher II\nHeavy Missile Launcher II", caldari, false},
		{"Caldari hull with railguns is racial filler", "[Scorpion, r]\n150mm Railgun II\n150mm Railgun II", caldari, false},
		{"Caldari hull with lasers is off-race", "[Scorpion, l]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II", caldari, true},
		{"a dual-race hull's doctrine is the union of both races", missilesAndRails, []WeaponSystem{SystemMissile, SystemHybrid}, false},
		{"one off-race weapon beside racial ones is filler", oneOffTwoOn, gallente, false},
		{"two off-race weapons beside racial ones are a loadout", twoOffTwoOn, gallente, true},
		{"unknown / pirate / ORE race has no doctrine, so no penalty", ishtarLaserEFT, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOffBonus(tc.eft, HullAffinity{Bonus: drone, Racial: tc.racial}))
		})
	}
}

// Launcher hardpoints make missile launchers acceptable filler on a drone-only hull
// whatever its race: Amarr Armageddon / Prophecy / Dragoon / Arbitrator launcher
// builds are hull-hardpoint meta, while a hull without launcher hardpoints (Ishtar,
// Dominix, Myrmidon) that carries missiles is off-race as before.
func TestIsOffBonus_droneHullLauncherHardpoints(t *testing.T) {
	var (
		drone    = []WeaponSystem{SystemDrone}
		amarr    = []WeaponSystem{SystemEnergy}
		gallente = []WeaponSystem{SystemHybrid}
	)
	lightMissiles := "[Arbitrator, m]\n" + strings.Repeat("Rapid Light Missile Launcher II\n", 3) + "Small Energy Neutralizer II"
	heavyMissiles := "[Armageddon, m]\n" + strings.Repeat("Rapid Heavy Missile Launcher II\n", 5)
	lasersAndMissiles := "[Prophecy, m]\n" + strings.Repeat("Heavy Pulse Laser II\n", 2) + strings.Repeat("Heavy Missile Launcher II\n", 2)
	blastersAndMissiles := "[Prophecy, m]\n" + strings.Repeat("Light Neutron Blaster II\n", 2) + strings.Repeat("Heavy Missile Launcher II\n", 2)
	lasers := "[Ishtar, l]\n" + strings.Repeat("Imperial Navy Small Focused Beam Laser\n", 4)
	missilesOnly := "[Ishtar, m]\nRocket Launcher II\nRocket Launcher II\nRocket Launcher II"

	tests := []struct {
		name      string
		eft       string
		racial    []WeaponSystem
		launchers bool
		want      bool
	}{
		{"Arbitrator (Amarr, launcher hardpoints) with light missiles is hull-hardpoint filler", lightMissiles, amarr, true, false},
		{"Armageddon (Amarr, launcher hardpoints) with heavy missiles", heavyMissiles, amarr, true, false},
		{"racial lasers and missiles together on a launcher hull", lasersAndMissiles, amarr, true, false},
		{"Amarr hull without launcher hardpoints with missiles is off-race", lightMissiles, amarr, false, true},
		{"Ishtar (Gallente, no launcher hardpoints) with missiles is off-race", missilesOnly, gallente, false, true},
		{"Ishtar with four lasers is off-race, launchers or not", lasers, gallente, false, true},
		{"launcher hardpoints do not excuse off-race turrets", lasers, gallente, true, true},
		{"blasters on an Amarr launcher hull stay off-race", blastersAndMissiles, amarr, true, true},
		{"unknown race with launcher hardpoints still has no doctrine, so no penalty", lightMissiles, nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hull := HullAffinity{Bonus: drone, Racial: tc.racial, Launchers: tc.launchers}
			require.Equal(t, tc.want, isOffBonus(tc.eft, hull))
		})
	}

	// Launcher hardpoints are read only for drone-only hulls.
	require.True(t, isOffBonus(lightMissiles, HullAffinity{Bonus: []WeaponSystem{SystemEnergy}, Launchers: true}),
		"missiles on a laser-bonused hull stay off-bonus whatever its hardpoints")
	require.False(t, isOffBonus(lightMissiles, HullAffinity{Bonus: []WeaponSystem{SystemEnergy, SystemDrone}}),
		"mixed drone hulls stay exempt")
}

func TestRacialSystems(t *testing.T) {
	require.Equal(t, []WeaponSystem{SystemEnergy}, RacialSystems(4), "Amarr")
	require.Equal(t, []WeaponSystem{SystemHybrid}, RacialSystems(8), "Gallente")
	require.Equal(t, []WeaponSystem{SystemProjectile}, RacialSystems(2), "Minmatar")
	require.Equal(t, []WeaponSystem{SystemHybrid, SystemMissile}, RacialSystems(1), "Caldari")
	require.Equal(t, []WeaponSystem{SystemHybrid, SystemMissile}, RacialSystems(1, 8), "Gila: Caldari + Gallente, deduplicated, fixed order")
	require.Equal(t, []WeaponSystem{SystemEnergy, SystemHybrid}, RacialSystems(8, 4), "order does not depend on the argument order")
	for _, other := range []int{0, 16, 32, 128, 135, 168, -1} {
		require.Nil(t, RacialSystems(other), "race %d fields no doctrine", other)
	}
	require.Nil(t, RacialSystems())
	require.Equal(t, []WeaponSystem{SystemEnergy}, RacialSystems(32, 4), "an unknown race beside a known one adds nothing")
}

func TestIsDroneHull(t *testing.T) {
	tests := []struct {
		name string
		hull []WeaponSystem
		want bool
	}{
		{"Ishtar — drones only", []WeaponSystem{SystemDrone}, true},
		{"Vexor — hybrid + drones", []WeaponSystem{SystemHybrid, SystemDrone}, false},
		{"Gila — missile + drones", []WeaponSystem{SystemMissile, SystemDrone}, false},
		{"Praxis — every system and drones", []WeaponSystem{SystemEnergy, SystemHybrid, SystemProjectile, SystemMissile, SystemDrone}, false},
		{"Rifter — projectile", []WeaponSystem{SystemProjectile}, false},
		{"no weapon bonus", nil, false},
		{"unknown hull", []WeaponSystem{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isDroneHull(tc.hull))
		})
	}
}

// realSDEHulls returns every published hull's trait texts, racial ship-skill race
// ids and launcher hardpoint counts from the real SDE, skipping the calling test
// when data/sde/sde.sqlite is absent (CI checkouts do not carry the 400 MB file).
func realSDEHulls(t *testing.T) (traits map[string][]string, races map[string][]int, launchers map[string]int) {
	t.Helper()
	p := sdetest.Path(t)
	s, err := sde.Open(p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	traits, races, launchers = s.ShipTraitTexts(), s.ShipRaceIDs(), s.ShipLauncherHardpoints()
	require.NotEmpty(t, traits)
	require.NotEmpty(t, races)
	require.NotEmpty(t, launchers)
	return traits, races, launchers
}

// The Q82 rule against the real hulls (2026-10 SDE): which hulls are drone-only,
// and what a four-laser / four-blaster / three-missile-launcher fit costs on each.
// Dominix, Myrmidon and Armageddon lost their turret bonus in the SDE, so they ARE
// drone-only hulls — but racial filler is legitimate meta: lasers on the Amarr
// Armageddon / Dragoon and blasters on the Gallente Dominix / Myrmidon are not
// penalised, the opposite system is, and so is anything on the Gallente Ishtar that
// is not a hybrid (Q82). Missile launchers are filler on every drone-only hull that
// has launcher hardpoints (Armageddon, Prophecy, Dragoon, Arbitrator) and off-race
// on those that have none (Ishtar, Dominix, Myrmidon). Vexor, Vexor Navy Issue and
// Gila keep a turret / missile bonus next to the drone bonus and stay exempt;
// Rifter and Punisher are plain weapon hulls.
func TestIsOffBonus_realSDEHulls(t *testing.T) {
	traits, races, launchers := realSDEHulls(t)
	lasers := "[X, lasers]\n" + strings.Repeat("Heavy Pulse Laser II\n", 4) + "\nDrone Damage Amplifier II"
	blasters := "[X, blasters]\n" + strings.Repeat("Mega Neutron Blaster Cannon II\n", 4) + "\nDrone Damage Amplifier II"
	missiles := "[X, missiles]\n" + strings.Repeat("Rapid Light Missile Launcher II\n", 3) + "\nDrone Damage Amplifier II"

	tests := []struct {
		hull            string
		wantDroneHull   bool
		wantRacial      []WeaponSystem
		wantLaunchers   bool
		wantLasersOff   bool
		wantBlastersOff bool
		wantMissilesOff bool
	}{
		{"Ishtar", true, []WeaponSystem{SystemHybrid}, false, true, false, true}, // Q82
		{"Dominix", true, []WeaponSystem{SystemHybrid}, false, true, false, true},
		{"Myrmidon", true, []WeaponSystem{SystemHybrid}, false, true, false, true},
		{"Eos", true, []WeaponSystem{SystemHybrid}, false, true, false, true},
		{"Armageddon", true, []WeaponSystem{SystemEnergy}, true, false, true, false},
		{"Dragoon", true, []WeaponSystem{SystemEnergy}, true, false, true, false},
		{"Prophecy", true, []WeaponSystem{SystemEnergy}, true, false, true, false},
		{"Arbitrator", true, []WeaponSystem{SystemEnergy}, true, false, true, false},
		{"Vexor", false, []WeaponSystem{SystemHybrid}, false, false, false, false},
		{"Vexor Navy Issue", false, []WeaponSystem{SystemHybrid}, false, false, false, false},
		{"Gila", false, []WeaponSystem{SystemHybrid, SystemMissile}, true, false, false, false},
		{"Rifter", false, []WeaponSystem{SystemProjectile}, true, true, true, true}, // projectile hull (the Q142 rule)
		{"Punisher", false, []WeaponSystem{SystemEnergy}, false, false, true, true}, // laser hull
	}
	for _, tc := range tests {
		t.Run(tc.hull, func(t *testing.T) {
			texts, ok := traits[tc.hull]
			require.True(t, ok, "%s has no trait rows in the SDE", tc.hull)
			hull := HullAffinity{
				Bonus:     WeaponSystemsFromTraits(texts),
				Racial:    RacialSystems(races[tc.hull]...),
				Launchers: launchers[tc.hull] > 0,
			}
			require.Equal(t, tc.wantDroneHull, isDroneHull(hull.Bonus), "bonused systems %v", hull.Bonus)
			require.Equal(t, tc.wantRacial, hull.Racial, "race ids %v", races[tc.hull])
			require.Equal(t, tc.wantLaunchers, hull.Launchers, "launcher hardpoints %d", launchers[tc.hull])
			require.Equal(t, tc.wantLasersOff, isOffBonus(lasers, hull), "lasers on %s: %+v", tc.hull, hull)
			require.Equal(t, tc.wantBlastersOff, isOffBonus(blasters, hull), "blasters on %s: %+v", tc.hull, hull)
			require.Equal(t, tc.wantMissilesOff, isOffBonus(missiles, hull), "missiles on %s: %+v", tc.hull, hull)
		})
	}

	// Every drone hull is drone-only in the SDE's own words: no trait text of it
	// names a turret / launcher system. The mixed hulls named in the brief are none.
	droneHulls := map[string]bool{}
	for hull, texts := range traits {
		if isDroneHull(WeaponSystemsFromTraits(texts)) {
			droneHulls[hull] = true
		}
	}
	for _, mixed := range []string{"Vexor", "Gila", "Tristan", "Ishkur", "Praxis", "Rattlesnake"} {
		require.False(t, droneHulls[mixed], mixed)
	}
	for _, drone := range []string{"Ishtar", "Eos", "Dominix", "Myrmidon", "Prophecy", "Dragoon", "Arbitrator"} {
		require.True(t, droneHulls[drone], drone)
	}
}

func TestMarkOffBonus_resolvesEachHullOnce(t *testing.T) {
	calls := map[string]int{}
	affinity := func(ship string) HullAffinity {
		calls[ship]++
		if ship == "Punisher" {
			return HullAffinity{Bonus: []WeaponSystem{SystemEnergy}}
		}
		return HullAffinity{}
	}
	hits := []FitSearchHit{
		{ShipName: "Punisher", EFT: punisherAutocannonEFT},
		{ShipName: "Punisher", EFT: "[Punisher, l]\nSmall Focused Pulse Laser II"},
		{ShipName: "Rifter", EFT: punisherAutocannonEFT},
		{ShipName: "Punisher", EFT: punisherAutocannonEFT},
	}
	markOffBonus(hits, affinity)
	require.Equal(t, []bool{true, false, false, true}, []bool{hits[0].offBonus, hits[1].offBonus, hits[2].offBonus, hits[3].offBonus})
	require.Equal(t, map[string]int{"Punisher": 1, "Rifter": 1}, calls)

	// A nil lookup is a no-op.
	clean := []FitSearchHit{{ShipName: "Punisher", EFT: punisherAutocannonEFT}}
	markOffBonus(clean, nil)
	require.False(t, clean[0].offBonus)
}

func TestBoostedComposite_offBonusPenalty(t *testing.T) {
	on := FitSearchHit{Score: 0.3, Views: 10}
	off := on
	off.offBonus = true

	require.InDelta(t, offBonusPenalty*boostedComposite(on, boostSpec{}), boostedComposite(off, boostSpec{}), 1e-12)
	require.Less(t, offBonusPenalty, 0.25, "must outweigh a single ×4 boost")

	// The widest quality spread inside one hull of the real corpus is ×8.9; give
	// the off-bonus fit that full advantage, the pvp-tackle ×4 boost and both
	// trust badges — it must still lose to a plain on-bonus fit.
	weakOn := FitSearchHit{Score: 0.1}
	strongOff := FitSearchHit{Score: 0.89, Tested: true, Video: true, offBonus: true, EFT: "[x, y]\nWarp Scrambler II"}
	spec := boostSpec{pvpTackle: true}
	require.Less(t, boostedComposite(strongOff, spec), boostedComposite(weakOn, spec))
}

// scrollBody renders a Qdrant scroll response holding the given hits.
func scrollBody(t *testing.T, pts []map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"result": map[string]any{"points": pts}})
	require.NoError(t, err)
	return string(raw)
}

func punisherPoint(id, name string, score float64, eft string, tags ...string) map[string]any {
	anyTags := make([]any, len(tags))
	for i, tg := range tags {
		anyTags[i] = tg
	}
	return map[string]any{"id": id, "payload": map[string]any{
		"ship_name": "Punisher", "fit_name": name, "source": "workbench", "score": score,
		"fit_tags": anyTags, "text": eft,
	}}
}

func TestSearchFits_offBonusFitLosesToOnBonus(t *testing.T) {
	laserEFT := "[Punisher, laser]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\n\n1MN Afterburner II"
	pts := []map[string]any{
		// Higher stored quality AND a real point (×4): wins without the penalty.
		punisherPoint("a", "AC brick", 0.30, punisherAutocannonEFT, "pvp"),
		punisherPoint("b", "laser", 0.10, laserEFT, "pvp"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(scrollBody(t, pts)))
	}))
	t.Cleanup(srv.Close)
	punisher := func(ship string) HullAffinity {
		if ship == "Punisher" {
			return HullAffinity{Bonus: []WeaponSystem{SystemEnergy}}
		}
		return HullAffinity{}
	}
	search := func(r *QdrantRetriever, q FitSearchQuery) []string {
		q.ShipName, q.Activity = "Punisher", "pvp"
		res, err := r.SearchFits(context.Background(), q)
		require.NoError(t, err)
		return hitNames(res.Hits)
	}
	plain := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	withAffinity := plain.WithWeaponAffinity(punisher)

	require.Equal(t, []string{"AC brick", "laser"}, search(plain, FitSearchQuery{}),
		"without an affinity lookup the off-bonus fit still wins on quality × tackle (the Q142 regression)")
	require.Equal(t, []string{"laser", "AC brick"}, search(withAffinity, FitSearchQuery{}),
		"the penalty re-orders — it does not filter: the off-bonus fit is still returned")

	// An explicit weapon-family request outranks the hull heuristic.
	require.Equal(t, "AC brick", search(withAffinity, FitSearchQuery{WeaponFamily: "autocannon"})[0])

	// WithWeaponAffinity copies — the original retriever stays penalty-free.
	require.Nil(t, plain.weaponAffinity)
	require.NotNil(t, withAffinity.weaponAffinity)
	require.Equal(t, plain.url, withAffinity.url)
	// ... and a derived retriever (per-request WithEmbed) keeps the lookup.
	require.NotNil(t, withAffinity.WithEmbed(fakeEmbed{}).weaponAffinity)
}

// Q82: the pool-wide ranking put a four-small-laser Ishtar (the corpus' best stored
// score, 0.54) on top of every drone-boat Ishtar. On a drone-only hull a weapon fit
// is down-weighted like an off-bonus one — it is still returned, and an explicit
// weapon request turns the penalty off.
func TestSearchFits_droneHullWeaponFitLosesToDroneFit(t *testing.T) {
	ishtarPoint := func(id, name string, score float64, eft string) map[string]any {
		return map[string]any{"id": id, "payload": map[string]any{
			"ship_name": "Ishtar", "fit_name": name, "source": "abysstracker", "score": score,
			"fit_tags": []any{"abyss", "pve"}, "text": eft,
		}}
	}
	pts := []map[string]any{
		ishtarPoint("a", "small lasers", 0.54, ishtarLaserEFT),
		ishtarPoint("b", "drone boat", 0.47, ishtarDroneEFT),
		ishtarPoint("c", "explorer", 0.30, ishtarLoneTurretEFT),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(scrollBody(t, pts)))
	}))
	t.Cleanup(srv.Close)
	ishtar := func(ship string) HullAffinity {
		if ship == "Ishtar" {
			return HullAffinity{
				Bonus: WeaponSystemsFromTraits([]string{
					"bonus to Sentry Drone hitpoints and damage", "bonus to Light Drone, Medium Drone, and Heavy Drone hitpoints and damage"}),
				Racial: RacialSystems(8), // Gallente Cruiser
			}
		}
		return HullAffinity{}
	}
	search := func(r *QdrantRetriever, q FitSearchQuery) []string {
		q.ShipName, q.Limit = "Ishtar", 5
		res, err := r.SearchFits(context.Background(), q)
		require.NoError(t, err)
		return hitNames(res.Hits)
	}
	plain := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	withAffinity := plain.WithWeaponAffinity(ishtar)

	require.Equal(t, []string{"small lasers", "drone boat", "explorer"}, search(plain, FitSearchQuery{}),
		"without an affinity lookup the highest stored score wins (the Q82 regression)")
	require.Equal(t, []string{"drone boat", "explorer", "small lasers"}, search(withAffinity, FitSearchQuery{}),
		"the weapon fit drops below every drone fit — and is still returned; a lone filler turret is not a weapon fit")
	require.Equal(t, []string{"drone boat", "explorer", "small lasers"},
		search(withAffinity, FitSearchQuery{FilamentType: "firestorm", Tag: "abyss", Tested: true}),
		"abyss / filament facets do not change the verdict")

	// An explicit weapon-family request outranks the hull heuristic.
	require.Equal(t, "small lasers", search(withAffinity, FitSearchQuery{WeaponFamily: "laser"})[0])
}

// Racial filler on a drone-only hull is legitimate meta: Amarr Dragoon / Armageddon
// laser fits and Gallente Dominix blaster fits keep their stored-quality order, and
// only the OFF-race fit of the same hull sinks.
func TestSearchFits_droneHullRacialFillerKeepsRank(t *testing.T) {
	point := func(id, ship, name string, score float64, eft string) map[string]any {
		return map[string]any{"id": id, "payload": map[string]any{
			"ship_name": ship, "fit_name": name, "source": "workbench", "score": score,
			"fit_tags": []any{"pve"}, "text": eft,
		}}
	}
	pulseLasers := "[Dragoon, pulse]\n" + strings.Repeat("Small Focused Pulse Laser II\n", 4) + "Small Energy Neutralizer II"
	blasters := "[Dragoon, blasters]\n" + strings.Repeat("Light Neutron Blaster II\n", 4)
	pts := []map[string]any{
		point("a", "Dragoon", "blasters", 0.40, blasters),
		point("b", "Dragoon", "pulse lasers", 0.30, pulseLasers),
		point("c", "Dragoon", "drones", 0.20, ishtarDroneEFT),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(scrollBody(t, pts)))
	}))
	t.Cleanup(srv.Close)
	dragoon := func(string) HullAffinity {
		return HullAffinity{Bonus: []WeaponSystem{SystemDrone}, Racial: RacialSystems(4)} // Amarr Destroyer
	}
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5).WithWeaponAffinity(dragoon)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Dragoon", Limit: 5})
	require.NoError(t, err)
	require.Equal(t, []string{"pulse lasers", "drones", "blasters"}, hitNames(res.Hits),
		"the Amarr hull keeps its laser fit on top of the drone fit; the off-race blaster fit, the best stored score, sinks")
}

// Launcher builds on a drone-only hull that has launcher hardpoints (the Amarr
// Arbitrator) are hull-hardpoint filler and keep their quality order; on a hull
// without launcher hardpoints the same fit sinks.
func TestSearchFits_droneHullLauncherHardpointsKeepRank(t *testing.T) {
	missiles := "[Arbitrator, missiles]\n" + strings.Repeat("Rapid Light Missile Launcher II\n", 3) + "Small Energy Neutralizer II"
	pts := []map[string]any{
		{"id": "a", "payload": map[string]any{"ship_name": "Arbitrator", "fit_name": "missiles", "source": "workbench", "score": 0.40, "fit_tags": []any{"pve"}, "text": missiles}},
		{"id": "b", "payload": map[string]any{"ship_name": "Arbitrator", "fit_name": "drones", "source": "workbench", "score": 0.20, "fit_tags": []any{"pve"}, "text": ishtarDroneEFT}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(scrollBody(t, pts)))
	}))
	t.Cleanup(srv.Close)
	search := func(launchers bool) []string {
		r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5).WithWeaponAffinity(func(string) HullAffinity {
			return HullAffinity{Bonus: []WeaponSystem{SystemDrone}, Racial: RacialSystems(4), Launchers: launchers}
		})
		res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Arbitrator", Limit: 5})
		require.NoError(t, err)
		return hitNames(res.Hits)
	}
	require.Equal(t, []string{"missiles", "drones"}, search(true))
	require.Equal(t, []string{"drones", "missiles"}, search(false))
}

func TestSearchFits_offBonusPenaltySkipsDroneHulls(t *testing.T) {
	// A hybrid + drone hull (Vexor): laser drone-boat fits are legitimate.
	laserEFT := "[Vexor, laser drone boat]\nSmall Focused Pulse Laser II\nSmall Focused Pulse Laser II\n\nHammerhead II x5"
	hybridEFT := "[Vexor, blaster]\nLight Neutron Blaster II\nLight Neutron Blaster II"
	pts := []map[string]any{
		{"id": "a", "payload": map[string]any{"ship_name": "Vexor", "fit_name": "laser drone boat", "source": "workbench", "score": 0.4, "fit_tags": []any{"pve"}, "text": laserEFT}},
		{"id": "b", "payload": map[string]any{"ship_name": "Vexor", "fit_name": "blaster", "source": "workbench", "score": 0.1, "fit_tags": []any{"pve"}, "text": hybridEFT}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(scrollBody(t, pts)))
	}))
	t.Cleanup(srv.Close)
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5).WithWeaponAffinity(func(string) HullAffinity {
		return HullAffinity{
			Bonus:  WeaponSystemsFromTraits([]string{"bonus to Medium Hybrid Turret damage", "bonus to Drone hitpoints, damage and mining yield"}),
			Racial: RacialSystems(8),
		}
	})
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Vexor", Activity: "pve", Limit: 5})
	require.NoError(t, err)
	require.Equal(t, []string{"laser drone boat", "blaster"}, hitNames(res.Hits), "quality order is untouched on a drone hull")
	for _, h := range res.Hits {
		require.False(t, h.offBonus, h.FitName)
	}
}

func TestFitSearchHit_offBonusIsNotSerialised(t *testing.T) {
	raw, err := json.Marshal(FitSearchHit{ShipName: "Punisher", offBonus: true})
	require.NoError(t, err)
	require.False(t, strings.Contains(strings.ToLower(string(raw)), "offbonus"), string(raw))
}
