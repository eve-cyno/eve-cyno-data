package bootstrap

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// writeTraitSDE builds a tiny SDE holding only what the affinity lookup reads:
// invTypes (name → typeID), invTraits, and the racial-skill chain (dgmTypeAttributes
// requiredSkill1 → a "<Race> <Class>" skill in group Spaceship Command → chrRaces),
// with the real column layout (TEXT ids in invTraits).
func writeTraitSDE(t *testing.T) *sde.SDE {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE invTypes (typeID INTEGER, groupID INTEGER, typeName TEXT, published INTEGER, mass REAL, volume REAL, capacity REAL)`,
		// Empty dogma tables keep the open-time required-column check quiet.
		`CREATE TABLE dgmEffects (effectID INTEGER, effectName TEXT, effectCategory INTEGER, modifierInfo TEXT)`,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER, attributeName TEXT, defaultValue REAL, stackable INTEGER, highIsGood INTEGER)`,
		`CREATE TABLE invGroups (groupID INTEGER, categoryID INTEGER, groupName TEXT)`,
		`INSERT INTO invGroups VALUES (25, 6, 'Frigate'), (26, 6, 'Cruiser'), (255, 16, 'Gunnery'), (257, 16, 'Spaceship Command')`,
		`CREATE TABLE dgmTypeAttributes (typeID INTEGER, attributeID INTEGER, valueInt REAL, valueFloat REAL)`,
		`CREATE TABLE chrRaces (raceID INTEGER, raceName TEXT, shortDescription TEXT)`,
		`INSERT INTO chrRaces VALUES (1, 'Caldari', NULL), (2, 'Minmatar', NULL), (4, 'Amarr', NULL), (8, 'Gallente', NULL), (32, 'Pirate', NULL)`,
		`CREATE TABLE invTraits (traitID TEXT, typeID TEXT, skillID TEXT, bonus TEXT, bonusText TEXT, unitID TEXT)`,
		`INSERT INTO invTypes VALUES (597, 25, 'Punisher', 1, 0, 0, 0), (626, 26, 'Vexor', 1, 0, 0, 0), (12005, 26, 'Ishtar', 1, 0, 0, 0),
			(32880, 25, 'Venture', 1, 0, 0, 0), (3331, 257, 'Amarr Frigate', 1, 0, 0, 0), (3332, 257, 'Gallente Cruiser', 1, 0, 0, 0),
			(3333, 255, 'Caldari Encryption Methods', 1, 0, 0, 0), (3334, 257, 'Spaceship Command', 1, 0, 0, 0),
			(3335, 26, 'Gila', 1, 0, 0, 0), (3336, 257, 'Caldari Cruiser', 1, 0, 0, 0),
			(99001, 25, 'Unpublished Laser Frigate', 0, 0, 0, 0)`,
		// launcherSlotsLeft (101): Gila 4, Punisher 2, Vexor and Ishtar 0 (a skill type carrying the
		// attribute is never read). requiredSkill1 (182) / requiredSkill2 (183): Punisher flies on Amarr Frigate, Vexor and
		// Ishtar on Gallente Cruiser, Gila on both Caldari and Gallente Cruiser, Venture on a
		// skill with no race. A skill's own prerequisite and a non-ship-handling skill that
		// merely starts with a race name are never read as a hull race.
		`INSERT INTO dgmTypeAttributes VALUES (597, 182, 3331, NULL), (626, 182, 3332, NULL), (12005, 182, NULL, 3332.0),
			(3335, 182, 3336, NULL), (3335, 183, 3332, NULL), (32880, 182, 3334, NULL),
			(99001, 182, 3331, NULL), (3331, 182, 3332, NULL), (597, 184, 3333, NULL),
			(3335, 101, NULL, 4.0), (12005, 101, NULL, 0.0), (626, 101, 0, NULL), (597, 101, 2, NULL),
			(3332, 101, 6, NULL)`,
		`INSERT INTO invTraits VALUES
			('41', '597', '3331', '10.0', 'reduction in <a href=showinfo:3303>Small Energy Turret</a> activation cost', '105'),
			('42', '597', '3331', '4.0', 'bonus to all armor resistances', '105'),
			('51', '626', '3332', '5.0', 'bonus to <a href=showinfo:3304>Medium Hybrid Turret</a> damage', '105'),
			('52', '626', '3332', '10.0', 'bonus to <a href=showinfo:3436>Drone</a> hitpoints, damage and mining yield', '105'),
			('61', '12005', '3332', '10.0', 'bonus to <a href=showinfo:24241>Light Drone</a> hitpoints and damage', '105'),
			('71', '32880', '-1', '', 'Can fit Ice Mining Laser modules', ''),
			('81', '99001', '3331', '5.0', 'bonus to Small Energy Turret damage', '105'),
			('91', '3331', '-1', '', 'bonus to Small Energy Turret damage', ''),
			('101', '3335', '3336', '5.0', 'bonus to kinetic and thermal missile damage', '105'),
			('102', '3335', '3336', '5.0', 'bonus to Medium Combat Drone damage', '105')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	s, err := sde.OpenWithReload(path, -1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHullWeaponAffinity(t *testing.T) {
	affinity := hullWeaponAffinity(writeTraitSDE(t))

	require.Equal(t, rag.HullAffinity{
		Bonus: []rag.WeaponSystem{rag.SystemEnergy}, Racial: []rag.WeaponSystem{rag.SystemEnergy}, Launchers: true}, affinity("Punisher"),
		"a hull with launcher hardpoints; the racial skill's own launcher attribute and the non-racial Caldari Encryption Methods requirement change nothing")
	require.Equal(t, rag.HullAffinity{
		Bonus: []rag.WeaponSystem{rag.SystemHybrid, rag.SystemDrone}, Racial: []rag.WeaponSystem{rag.SystemHybrid}}, affinity("Vexor"))
	require.Equal(t, rag.HullAffinity{
		Bonus: []rag.WeaponSystem{rag.SystemDrone}, Racial: []rag.WeaponSystem{rag.SystemHybrid}}, affinity("Ishtar"),
		"a drone-only hull carries its race's doctrine (Gallente Cruiser → hybrid), read through a REAL-typed skill id too; "+
			"zero launcher hardpoints (a stored 0.0) is no launcher hull")
	require.Equal(t, rag.HullAffinity{
		Bonus: []rag.WeaponSystem{rag.SystemMissile, rag.SystemDrone}, Racial: []rag.WeaponSystem{rag.SystemHybrid, rag.SystemMissile},
		Launchers: true}, affinity("Gila"),
		"a hull flown on two racial skills fields both doctrines")
	require.Equal(t, rag.WeaponSystemsFromTraits([]string{"bonus to Small Energy Turret damage"}), affinity("  punisher ").Bonus,
		"name match is case-insensitive and trimmed")

	require.Zero(t, affinity("Venture"), "a hull with traits but no weapon bonus")
	require.Zero(t, affinity("Amarr Frigate"), "only ship hulls are read, never a skill type that carries trait rows")
	require.Zero(t, affinity("Unpublished Laser Frigate"), "unpublished hulls are not read")
	require.Zero(t, affinity("Not A Ship"), "unknown name")
	require.Zero(t, affinity(""))
}

// The real SDE: the hulls the QC2 investigation measured (2026-10 SDE).
func TestHullWeaponAffinity_realSDE(t *testing.T) {
	cfg := corpusAvailable(t)
	s, err := sde.Open(cfg.SDEPath)
	require.NoError(t, err)
	defer s.Close()
	affinity := hullWeaponAffinity(s)

	want := map[string][]rag.WeaponSystem{
		"Punisher":    {rag.SystemEnergy},
		"Retribution": {rag.SystemEnergy},
		"Stabber":     {rag.SystemProjectile},
		"Rifter":      {rag.SystemProjectile},
		"Vexor":       {rag.SystemHybrid, rag.SystemDrone},
		"Tristan":     {rag.SystemHybrid, rag.SystemDrone},
		"Caracal":     {rag.SystemMissile},
		"Drake":       {rag.SystemMissile},
		"Hawk":        {rag.SystemMissile},
		"Gila":        {rag.SystemMissile, rag.SystemDrone},
		"Ishtar":      {rag.SystemDrone},
		"Dominix":     {rag.SystemDrone}, // the hybrid bonus left it: a drone-only hull (Q82 rule)
		"Myrmidon":    {rag.SystemDrone},
		"Armageddon":  {rag.SystemDrone},
		"Dragoon":     {rag.SystemDrone},
		"Damavik":     {rag.SystemPrecursor},
	}
	for ship, systems := range want {
		require.Equal(t, systems, affinity(ship).Bonus, ship)
	}

	// The race doctrine of the drone-only hulls (Q82): racial filler is not off-bonus.
	racial := map[string][]rag.WeaponSystem{
		"Ishtar":     {rag.SystemHybrid}, // Gallente Cruiser
		"Dominix":    {rag.SystemHybrid},
		"Myrmidon":   {rag.SystemHybrid},
		"Dragoon":    {rag.SystemEnergy}, // Amarr Destroyer
		"Armageddon": {rag.SystemEnergy},
		"Gila":       {rag.SystemHybrid, rag.SystemMissile}, // Caldari + Gallente Cruiser
		"Rifter":     {rag.SystemProjectile},
		"Caracal":    {rag.SystemMissile, rag.SystemHybrid},
	}
	for ship, systems := range racial {
		got := affinity(ship).Racial
		require.ElementsMatch(t, systems, got, ship)
	}

	// Launcher hardpoints (dogma 101): missile launchers are hull-hardpoint filler on
	// the drone-only hulls that have them (Q82 refinement).
	launchers := map[string]bool{
		"Armageddon": true, "Prophecy": true, "Dragoon": true, "Arbitrator": true, "Gila": true,
		"Ishtar": false, "Dominix": false, "Myrmidon": false, "Vexor": false, "Punisher": false, "Rifter": true,
	}
	for ship, want := range launchers {
		require.Equal(t, want, affinity(ship).Launchers, ship)
	}
}

func TestCachedHullAffinity_loadsOncePerTTL(t *testing.T) {
	loads := 0
	data := map[string][]string{"Punisher": {"bonus to Small Energy Turret damage"}}
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := newCachedHullAffinity(func() map[string][]string {
		loads++
		return data
	}, time.Minute, func() time.Time { return clock })

	for i := 0; i < 50; i++ {
		require.Equal(t, []rag.WeaponSystem{rag.SystemEnergy}, c.lookup("Punisher").Bonus)
	}
	require.Equal(t, 1, loads, "one table build serves every lookup inside the TTL")

	// SDE rebuilt: the hull gains a hybrid bonus — picked up only after the TTL.
	data = map[string][]string{"Punisher": {"bonus to Small Hybrid Turret damage"}}
	clock = clock.Add(30 * time.Second)
	require.Equal(t, []rag.WeaponSystem{rag.SystemEnergy}, c.lookup("Punisher").Bonus)
	clock = clock.Add(31 * time.Second)
	require.Equal(t, []rag.WeaponSystem{rag.SystemHybrid}, c.lookup("Punisher").Bonus)
	require.Equal(t, 2, loads)
}

func TestCachedHullAffinity_concurrentLookups(t *testing.T) {
	loads := 0
	c := newCachedHullAffinity(func() map[string][]string {
		loads++
		return map[string][]string{"Gila": {"bonus to kinetic and thermal missile damage", "bonus to Medium Combat Drone damage"}}
	}, time.Hour, time.Now)

	const workers = 32
	gila := make([]rag.HullAffinity, workers)
	rifter := make([]rag.HullAffinity, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gila[i], rifter[i] = c.lookup("gila"), c.lookup("Rifter")
		}()
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		require.Equal(t, []rag.WeaponSystem{rag.SystemMissile, rag.SystemDrone}, gila[i].Bonus)
		require.Zero(t, rifter[i])
	}
	require.Equal(t, 1, loads)
}

// The race table is cached and rebuilt together with the trait table; without a race
// source every hull simply carries no doctrine.
func TestCachedHullAffinity_racesFollowTheSameTTL(t *testing.T) {
	traitLoads, raceLoads := 0, 0
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	traits := func() map[string][]string {
		traitLoads++
		return map[string][]string{"Ishtar": {"bonus to Heavy Drone hitpoints and damage"}}
	}
	c := newCachedHullAffinity(traits, time.Minute, func() time.Time { return clock }).withRaces(func() map[string][]int {
		raceLoads++
		return map[string][]int{"Ishtar": {8}, "Unknown Hull": {4}}
	})
	for i := 0; i < 20; i++ {
		require.Equal(t, rag.HullAffinity{
			Bonus: []rag.WeaponSystem{rag.SystemDrone}, Racial: []rag.WeaponSystem{rag.SystemHybrid}}, c.lookup("ishtar"))
	}
	require.Zero(t, c.lookup("Unknown Hull"), "a race without a weapon bonus is not an entry")
	require.Equal(t, [2]int{1, 1}, [2]int{traitLoads, raceLoads})
	clock = clock.Add(2 * time.Minute)
	c.lookup("Ishtar")
	require.Equal(t, [2]int{2, 2}, [2]int{traitLoads, raceLoads})

	noRaces := newCachedHullAffinity(traits, time.Minute, func() time.Time { return clock })
	require.Equal(t, rag.HullAffinity{Bonus: []rag.WeaponSystem{rag.SystemDrone}}, noRaces.lookup("Ishtar"))
}

// Launcher hardpoint counts are loaded with the same cache; a hull is a launcher hull
// at one hardpoint or more, and without a source no hull is one.
func TestCachedHullAffinity_launchersFollowTheSameTTL(t *testing.T) {
	traitLoads, launcherLoads := 0, 0
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	traits := func() map[string][]string {
		traitLoads++
		return map[string][]string{
			"Arbitrator": {"bonus to Drone hitpoints and damage"},
			"Ishtar":     {"bonus to Heavy Drone hitpoints and damage"},
		}
	}
	c := newCachedHullAffinity(traits, time.Minute, func() time.Time { return clock }).withLaunchers(func() map[string]int {
		launcherLoads++
		return map[string]int{"Arbitrator": 3, "Ishtar": 0}
	})
	for i := 0; i < 20; i++ {
		require.True(t, c.lookup("arbitrator").Launchers)
		require.False(t, c.lookup("Ishtar").Launchers)
	}
	require.Equal(t, [2]int{1, 1}, [2]int{traitLoads, launcherLoads})
	clock = clock.Add(2 * time.Minute)
	c.lookup("Ishtar")
	require.Equal(t, [2]int{2, 2}, [2]int{traitLoads, launcherLoads})

	none := newCachedHullAffinity(traits, time.Minute, func() time.Time { return clock })
	require.False(t, none.lookup("Arbitrator").Launchers)
}
