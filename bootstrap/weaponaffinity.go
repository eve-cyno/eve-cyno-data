package bootstrap

import (
	"strings"
	"sync"
	"time"

	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
)

// hullAffinityTTL bounds how stale the cached hull → weapon-system table may get.
// Hull traits only change with an SDE rebuild; the SDE handle itself checks for a
// rebuilt file every sde.DefaultReloadInterval, so a table at most this old
// follows a hot-reloaded SDE within one more TTL.
const hullAffinityTTL = 10 * time.Minute

// hullWeaponAffinity builds the rag.WeaponAffinity lookup the fit search uses to
// demote off-bonus fits (autocannons on the laser-bonused Punisher, eval Q142;
// small lasers on the Gallente, drone-bonused Ishtar, eval Q82): a hull's invTraits
// bonus texts go through rag.WeaponSystemsFromTraits to give the weapon systems it
// is bonused for (HullAffinity.Bonus). A hull whose only entry is rag.SystemDrone
// (Ishtar, Dominix, Myrmidon, …) has no on-bonus weapon; there the search demotes
// only weapons its race does not field, so its HullAffinity.Racial carries the
// race's doctrine (rag.RacialSystems of the hull's racial ship skill, see
// sde.ShipRaceIDs) — racial filler such as lasers on an Amarr Dragoon stays
// unpenalised — and HullAffinity.Launchers flags launcher hardpoints (see
// sde.ShipLauncherHardpoints), which make missile launchers filler too (Amarr
// Arbitrator, Prophecy, Armageddon). A drone + weapon hull (Vexor, Gila) stays exempt. Unknown hulls and
// hulls without weapon bonuses resolve to the zero HullAffinity, so they are never
// penalised.
//
// The whole hull table (≈360 hulls with traits, two ~1 ms queries) is loaded lazily
// and cached for hullAffinityTTL: a hull-less browse search ranks hits of ~300
// distinct hulls, and one SDE name-resolve plus trait query per hull cost ~12 ms
// each (3.8 s per search measured on the prod mirror).
func hullWeaponAffinity(s *sde.SDE) rag.WeaponAffinity {
	return newCachedHullAffinity(s.ShipTraitTexts, hullAffinityTTL, time.Now).
		withRaces(s.ShipRaceIDs).withLaunchers(s.ShipLauncherHardpoints).lookup
}

// cachedHullAffinity serves hull → HullAffinity from a table rebuilt from load (and
// races) at most once per ttl. Safe for concurrent use (SearchFits runs per request).
type cachedHullAffinity struct {
	load  func() map[string][]string // hull typeName → trait texts
	races func() map[string][]int    // hull typeName → racial ship-skill race ids; nil → no doctrine
	// launchers: hull typeName → launcher hardpoint count (hulls with none omitted); nil → none known.
	launchers func() map[string]int
	ttl       time.Duration
	now       func() time.Time

	mu     sync.Mutex
	loaded time.Time
	byHull map[string]rag.HullAffinity // lower-cased hull name → affinity (hulls without a weapon bonus omitted)
}

func newCachedHullAffinity(load func() map[string][]string, ttl time.Duration, now func() time.Time) *cachedHullAffinity {
	return &cachedHullAffinity{load: load, ttl: ttl, now: now}
}

// withRaces adds the source of each hull's race ids (see sde.ShipRaceIDs); it must
// be called before the first lookup.
func (c *cachedHullAffinity) withRaces(races func() map[string][]int) *cachedHullAffinity {
	c.races = races
	return c
}

// withLaunchers adds the source of each hull's launcher hardpoint count (see
// sde.ShipLauncherHardpoints); it must be called before the first lookup.
func (c *cachedHullAffinity) withLaunchers(launchers func() map[string]int) *cachedHullAffinity {
	c.launchers = launchers
	return c
}

// lookup implements rag.WeaponAffinity. The name match is case-insensitive.
func (c *cachedHullAffinity) lookup(shipName string) rag.HullAffinity {
	key := strings.ToLower(strings.TrimSpace(shipName))
	if key == "" {
		return rag.HullAffinity{}
	}
	return c.table()[key]
}

// table returns the current table, rebuilding it when it is missing or older than
// ttl. The returned map is never mutated after publication, so callers read it
// without holding the lock.
func (c *cachedHullAffinity) table() map[string]rag.HullAffinity {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHull != nil && c.now().Sub(c.loaded) < c.ttl {
		return c.byHull
	}
	var races map[string][]int
	if c.races != nil {
		races = c.races()
	}
	var launchers map[string]int
	if c.launchers != nil {
		launchers = c.launchers()
	}
	byHull := map[string]rag.HullAffinity{}
	for hull, texts := range c.load() {
		if systems := rag.WeaponSystemsFromTraits(texts); len(systems) > 0 {
			byHull[strings.ToLower(hull)] = rag.HullAffinity{
				Bonus:     systems,
				Racial:    rag.RacialSystems(races[hull]...),
				Launchers: launchers[hull] > 0,
			}
		}
	}
	c.byHull, c.loaded = byHull, c.now()
	return c.byHull
}
