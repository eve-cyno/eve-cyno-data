package rag

import "strings"

// WeaponSystem is a turret / launcher family that a hull bonus can name and that
// a fit's weapon modules belong to. It is deliberately coarse (the four classic
// systems plus the Triglavian one): hull bonuses are written per size and per
// missile type ("Medium Hybrid Turret", "Heavy Assault Missile") but the
// distinction that matters for ranking is "lasers vs blasters/rails vs
// autocannons/artillery vs missiles".
type WeaponSystem string

const (
	SystemEnergy     WeaponSystem = "energy"     // pulse / beam lasers
	SystemHybrid     WeaponSystem = "hybrid"     // blasters, railguns
	SystemProjectile WeaponSystem = "projectile" // autocannons, artillery
	SystemMissile    WeaponSystem = "missile"    // rockets … torpedoes (any launcher family)
	SystemPrecursor  WeaponSystem = "precursor"  // Triglavian disintegrators, Vorton projectors

	// SystemDrone is never a module classification — it marks a HULL whose bonuses
	// include drones (see WeaponSystemsFromTraits). Such a hull's damage comes from
	// its drone bay. When drones are its ONLY weapon bonus (isDroneHull) an
	// off-race weapon loadout on it is off-bonus; when it also carries a turret /
	// launcher bonus any weapon on it is exempt (see isOffBonus).
	SystemDrone WeaponSystem = "drone"
)

// HullAffinity is what the retriever knows about a hull's weapons.
type HullAffinity struct {
	// Bonus is the weapon systems the hull's role/skill bonuses name (see
	// WeaponSystemsFromTraits); empty when the hull is unknown or carries no weapon
	// bonus.
	Bonus []WeaponSystem
	// Racial is the hull race's weapon doctrine (see RacialSystems). It is read only
	// for a drone-only hull (isDroneHull), whose bonuses name no weapon system:
	// there it separates racial filler (lasers on an Amarr hull) from an off-race
	// loadout. Empty when the race is unknown or fields no doctrine.
	Racial []WeaponSystem
	// Launchers is true when the hull has launcher hardpoints (dogma 101 > 0). Like
	// Racial it is read only for a drone-only hull: missile launchers there are
	// hull-hardpoint filler, in doctrine whatever the hull's race.
	Launchers bool
}

// WeaponAffinity resolves a hull — by exact ship_name, as stored on the fit
// points — to its HullAffinity. The zero HullAffinity means the hull is unknown or
// carries no weapon bonus. It is injected at construction time (core/bootstrap
// derives it from the SDE's invTraits and racial ship skills) because the
// retriever itself has no SDE access; a nil WeaponAffinity disables the off-bonus
// ranking penalty entirely.
type WeaponAffinity func(shipName string) HullAffinity

// chrRaces ids of the four empire races.
const (
	raceCaldari  = 1
	raceMinmatar = 2
	raceAmarr    = 4
	raceGallente = 8
)

// RacialSystems returns the weapon systems the given races field, deduplicated and
// in a fixed order: Amarr → lasers, Gallente → blasters / railguns, Minmatar →
// autocannons / artillery, Caldari → missiles and hybrids. Several ids are the
// union (a pirate hull flown on two racial skills, e.g. Gila: Caldari + Gallente
// Cruiser). Any other race — pirate, ORE, Triglavian, unknown — fields no
// doctrine, and the result is nil when no id is an empire race.
func RacialSystems(raceIDs ...int) []WeaponSystem {
	fielded := map[WeaponSystem]bool{}
	for _, id := range raceIDs {
		switch id {
		case raceAmarr:
			fielded[SystemEnergy] = true
		case raceGallente:
			fielded[SystemHybrid] = true
		case raceMinmatar:
			fielded[SystemProjectile] = true
		case raceCaldari:
			fielded[SystemMissile], fielded[SystemHybrid] = true, true
		}
	}
	var out []WeaponSystem
	for _, s := range []WeaponSystem{SystemEnergy, SystemHybrid, SystemProjectile, SystemMissile} {
		if fielded[s] {
			out = append(out, s)
		}
	}
	return out
}

// offBonusPenalty is the composite-score multiplier of a fit whose main weapon
// system is not one of its hull's bonused systems (see isOffBonus). It is a
// down-weight, not a filter: an off-bonus fit is still returned when nothing
// on-bonus matches the request.
//
// 0.02 (÷50) is sized against the real corpus (7,608 fits, 2026-10-06): the
// widest quality spread inside one hull is ×8.9 (composite = score × views term;
// p99 across hulls ×7.6), and one ×4 facet boost plus the ×1.2 trust badges
// stretch that to ≈ ×43 — so an off-bonus fit loses to every on-bonus fit that
// merely lacks one ×4 boost, whatever the stored quality of either. It can still
// win by holding two stacked ×4 matches (e.g. tackle AND the requested tank
// type) against an on-bonus fit holding none: the user's explicit constraints
// outrank the hull heuristic.
const offBonusPenalty = 0.02

// WeaponSystemsFromTraits derives the weapon systems a hull's bonuses name from
// its SDE trait texts (invTraits.bonusText with the <a href> wrappers stripped,
// e.g. "bonus to Small Energy Turret damage",
// "bonus to kinetic and thermal missile damage",
// "bonus to Drone hitpoints and damage"). The result is deduplicated and in a
// fixed order; it is nil when no text names a weapon system. A text that
// describes a drawback ("penalty to missile flight time") names nothing.
//
// Checked against all 778 distinct trait texts of the 2026-10 SDE: every text that
// mentions a turret system, a missile / rocket / torpedo, a disintegrator, a vorton
// projector or a drone is classified; the only unmatched "turret" texts are
// hardpoint notes ("+7 high slots, +5 turret hardpoints"), not bonuses. Of the 360
// hulls in the fit corpus, 237 carry a turret / launcher bonus (30 of them a drone
// bonus too, which exempts them — see isOffBonus) and 33 are drone-only hulls
// (isDroneHull).
func WeaponSystemsFromTraits(texts []string) []WeaponSystem {
	var out []WeaponSystem
	for _, g := range traitSystemMarkers {
		if traitsMention(texts, g.markers) {
			out = append(out, g.system)
		}
	}
	return out
}

// traitSystemMarkers are the lower-case fragments of a trait text that name a
// weapon system, in the fixed order WeaponSystemsFromTraits reports them.
var traitSystemMarkers = []struct {
	system  WeaponSystem
	markers []string
}{
	{SystemEnergy, []string{"energy turret"}},
	{SystemHybrid, []string{"hybrid turret"}},
	{SystemProjectile, []string{"projectile turret", "projectile weapon"}},
	{SystemMissile, []string{"missile", "rocket", "torpedo", "cruise"}},
	{SystemPrecursor, []string{"disintegrator", "vorton projector"}},
	{SystemDrone, []string{"drone"}},
}

// traitsMention reports whether any bonus text (drawbacks excluded) contains any
// of the lower-case markers.
func traitsMention(texts, markers []string) bool {
	for _, raw := range texts {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" || strings.HasPrefix(t, "penalty") || strings.HasPrefix(t, "decrease") {
			continue
		}
		for _, m := range markers {
			if strings.Contains(t, m) {
				return true
			}
		}
	}
	return false
}

// moduleSystemMarkers classifies a module NAME (an EFT line without its charge)
// into the weapon system it belongs to. Checked in order, first hit wins. The
// lists cover every published turret / launcher in the SDE except four legacy
// "Lux …" lasers; a name no marker matches is simply unclassified — which is the
// safe direction, because an unclassified module can never make a fit look
// off-bonus. Verified against the SDE module groups (Energy / Hybrid /
// Projectile Weapon, every Missile Launcher group, Precursor Weapon, Vorton
// Projector) and against the module names of the corpus: no non-weapon module
// matches, and the meta-variant names ("Modulated Pulse Energy Beam", "Prototype
// Gauss Gun", "Gallium Machine Gun", "'Limos' Heavy Missile Bay", …) are covered.
// On the 7,608 corpus fits this classifies 21,269 turret / launcher modules.
var moduleSystemMarkers = []struct {
	system  WeaponSystem
	markers []string
}{
	{SystemPrecursor, []string{"Disintegrator", "Vorton Projector"}},
	{SystemEnergy, []string{"Laser", "Energy Beam", "Particle Stream"}},
	{SystemHybrid, []string{"Blaster", "Railgun", "Gatling Rail", "Particle Cannon", "Accelerator Cannon",
		"Gauss Gun", "Particle Accelerator", "Phase Cannon", "Coil Gun"}},
	{SystemProjectile, []string{"AutoCannon", "Autocannon", "Automatic Cannon", "Repeating Cannon", "Artillery",
		"Howitzer", "Siege Cannon", "Machine Gun", "Gallium Cannon"}},
	{SystemMissile, []string{"Missile Launcher", "Rocket Launcher", "Torpedo Launcher", "Cruise Launcher",
		"Missile Bay", "Rocket Bay", "Torpedo Bay"}},
}

// nonWeaponLaserMarkers are module-name fragments that contain "Laser" but are
// not weapons: mining lasers, their upgrades and the mining foreman link.
var nonWeaponLaserMarkers = []string{"Mining Laser", "Laser Optimization"}

// moduleWeaponSystem returns the weapon system of a module name, or "" when it is
// not a turret / launcher (probe, bomb, defender, interdiction-sphere and festival
// launchers, smartbombs, mining lasers, drones, charges, …).
func moduleWeaponSystem(name string) WeaponSystem {
	for _, nw := range nonWeaponLaserMarkers {
		if strings.Contains(name, nw) {
			return ""
		}
	}
	for _, g := range moduleSystemMarkers {
		for _, m := range g.markers {
			if strings.Contains(name, m) {
				return g.system
			}
		}
	}
	return ""
}

// fitWeaponCounts counts the turret / launcher modules of a fit's EFT block per
// weapon system. Only the lines AFTER the "[Hull, Fit name]" header are read: the
// stored text of a community fit starts with a metadata preamble ("Author: … |
// Tags: Blaster") that must never be mistaken for a module. A text without an EFT
// header yields nil. Module lines may carry a charge ("Heavy Pulse Laser II,
// Multifrequency M") and an offline marker; both are cut off.
func fitWeaponCounts(eft string) map[WeaponSystem]int {
	lines := strings.Split(eft, "\n")
	start := -1
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") && strings.Contains(l, ",") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var counts map[WeaponSystem]int
	for _, l := range lines[start+1:] {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "[") {
			continue
		}
		name, _, _ := strings.Cut(l, ",")
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "/OFFLINE"))
		if sys := moduleWeaponSystem(name); sys != "" {
			if counts == nil {
				counts = map[WeaponSystem]int{}
			}
			counts[sys]++
		}
	}
	return counts
}

// mainWeaponSystems returns the weapon system(s) a fit leans on: those with the
// highest module count. More than one is returned only on an exact tie.
func mainWeaponSystems(counts map[WeaponSystem]int) []WeaponSystem {
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	if max == 0 {
		return nil
	}
	var out []WeaponSystem
	for _, s := range []WeaponSystem{SystemEnergy, SystemHybrid, SystemProjectile, SystemMissile, SystemPrecursor} {
		if counts[s] == max {
			out = append(out, s)
		}
	}
	return out
}

// droneHullMinWeapons is how many classified OFF-RACE turret / launcher modules
// make a fit a weapon fit on a drone-only hull (see isDroneHull, isOffBonus). One
// is not enough: the 11 single-weapon fits of the corpus' drone-only hulls
// (2026-10-06) all hold a lone turret or launcher among tractor-beam / salvager /
// cloak / probe / neutralizer highs — filler, not a damage loadout — whereas two
// or more (3-6, mostly 4) are a gun or launcher build.
const droneHullMinWeapons = 2

// isDroneHull reports whether the hull's bonuses name drones and NO turret /
// launcher system — its damage comes from the drone bay and none of its weapons
// is on-bonus. A hull that also carries a weapon bonus (Vexor: hybrid + drones,
// Gila: missiles + drones, Praxis, …) is not one: its turrets / launchers may be
// on-bonus. In the 2026-10 SDE 38 hulls qualify, 33 of them in the fit corpus —
// among them Ishtar, Eos, Dominix, Myrmidon, Armageddon, Prophecy, Dragoon,
// Arbitrator, Curse and the industrial / logistics drone hulls (Orca, Rorqual,
// Scimitar, …) that carry no weapons anyway.
func isDroneHull(hullBonus []WeaponSystem) bool {
	drone := false
	for _, s := range hullBonus {
		if s != SystemDrone {
			return false
		}
		drone = true
	}
	return drone
}

// offRaceWeaponCount is the number of classified turret / launcher modules whose
// system is not one of the racial systems.
func offRaceWeaponCount(counts map[WeaponSystem]int, racial []WeaponSystem) int {
	fielded := make(map[WeaponSystem]bool, len(racial))
	for _, s := range racial {
		fielded[s] = true
	}
	n := 0
	for s, c := range counts {
		if !fielded[s] {
			n += c
		}
	}
	return n
}

// isOffBonus reports whether the fit's weapons are NOT what the hull is bonused
// for — e.g. autocannons on the laser-bonused Punisher, or a four-small-laser
// Ishtar (eval Q82). It is conservative on every uncertain input, so a missing
// signal never costs a fit its rank:
//   - a hull with no weapon bonus (or an unknown hull) has nothing to be off;
//   - a drone-only hull (isDroneHull) has no on-bonus weapon, but racial filler is
//     legitimate meta (lasers on an Amarr Armageddon / Dragoon, blasters on a
//     Gallente Dominix / Myrmidon), so a fit is off-bonus only when it carries
//     droneHullMinWeapons or more turrets / launchers of a system the hull's race
//     does not field (hull.Racial; Gallente Ishtar + lasers, Dominix + lasers).
//     Missile launchers are in doctrine on a hull with launcher hardpoints
//     (hull.Launchers: Amarr Armageddon / Prophecy / Dragoon / Arbitrator launcher
//     builds are hull-hardpoint meta) and off-race on one without (Ishtar, Dominix,
//     Myrmidon). A hull whose race is unknown or fields no doctrine is never
//     penalised. Drone fits — drones, drone-damage modules, utility highs such as
//     neutralizers, nosferatus, probe launchers, cloaks, tractors, salvagers — and
//     fits with a lone filler weapon are not penalised either. On the 7,608 corpus
//     fits this marks 51 fits beyond the 40 of the weapon-bonus rule (Dragoon 18,
//     Ishtar 17, Magus 6, Myrmidon 4, Pontifex 2, …), almost all of them
//     projectile turrets (autocannons, artillery) on Gallente and Amarr hulls,
//     plus lasers on the Ishtar;
//   - any other hull with a drone bonus is exempt: it also carries a weapon bonus,
//     so its damage may come from either the drone bay or the guns (10 of the 43
//     Vexor fits carry lasers or autocannons on a hybrid + drone hull and are
//     legitimate drone boats);
//   - a fit without any classifiable turret / launcher is not penalised;
//   - on an exact tie between systems the fit counts as on-bonus if ANY of the
//     tied systems is bonused.
func isOffBonus(eft string, hull HullAffinity) bool {
	if len(hull.Bonus) == 0 {
		return false
	}
	if isDroneHull(hull.Bonus) {
		if len(hull.Racial) == 0 {
			return false
		}
		doctrine := hull.Racial
		if hull.Launchers {
			doctrine = append(append([]WeaponSystem(nil), doctrine...), SystemMissile)
		}
		return offRaceWeaponCount(fitWeaponCounts(eft), doctrine) >= droneHullMinWeapons
	}
	bonused := make(map[WeaponSystem]bool, len(hull.Bonus))
	for _, s := range hull.Bonus {
		if s == SystemDrone {
			return false
		}
		bonused[s] = true
	}
	main := mainWeaponSystems(fitWeaponCounts(eft))
	if len(main) == 0 {
		return false
	}
	for _, s := range main {
		if bonused[s] {
			return false
		}
	}
	return true
}

// markOffBonus stamps FitSearchHit.offBonus on every hit of the pool using the
// hull lookup. Each distinct hull is resolved once per call. A nil lookup is a
// no-op (no penalty).
func markOffBonus(hits []FitSearchHit, affinity WeaponAffinity) {
	if affinity == nil {
		return
	}
	byHull := map[string]HullAffinity{}
	for i := range hits {
		hull, seen := byHull[hits[i].ShipName]
		if !seen {
			hull = affinity(hits[i].ShipName)
			byHull[hits[i].ShipName] = hull
		}
		hits[i].offBonus = isOffBonus(hits[i].EFT, hull)
	}
}
