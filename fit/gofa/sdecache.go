package gofa

import (
	"maps"
	"sync"
	"sync/atomic"

	"eve-cyno.dev/go/data/sde"
)

// The dogma interpreter is read-heavy on static data: one Stats call touches
// ~530 items (the fit plus every published skill at level V) and re-reads each
// item's effects, modifiers and group on every fixpoint pass. Against SQLite that
// was ~27k single-row queries per call (≈1.3 s); the same lookups served from
// memory are nanoseconds. cachedSDE memoizes them, behind sync.Map so concurrent
// Stats calls (core-api serves concurrent turns) share one cache without a
// lock on the read path.
//
// Static data only: nothing here depends on the fit, so a cached value is valid
// for every call until the SDE file itself is replaced. That is tracked by
// generation (see generationer): a hot-reloaded SDE gets a fresh cachedSDE.

// generationer is implemented by SDE handles that follow a replaced backing file
// (*sde.SDE bumps its generation on every hot reload). An SDE without it (a test
// stub) is treated as immutable.
type generationer interface{ Generation() uint64 }

// maxMemoEntries caps one per-type memo. The working set is the fitted types plus
// the ~512 skills (a few thousand entries at most); the cap only bounds memory
// when callers walk a large part of the ~50k-type SDE. Past it, lookups still
// work, they are just not stored.
const maxMemoEntries = 16384

// memo is a concurrent int-keyed read-through cache. A racing first load may run
// load twice; both results are equal (static data) and LoadOrStore keeps one.
type memo[V any] struct {
	m sync.Map // int → V
	n atomic.Int64
}

func (c *memo[V]) lookup(key int) (V, bool) {
	v, ok := c.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// store keeps v unless the memo is full, and returns the value now cached.
func (c *memo[V]) store(key int, v V) V {
	if c.n.Load() >= maxMemoEntries {
		return v
	}
	actual, loaded := c.m.LoadOrStore(key, v)
	if !loaded {
		c.n.Add(1)
	}
	return actual.(V)
}

func (c *memo[V]) get(key int, load func(int) V) V {
	if v, ok := c.lookup(key); ok {
		return v
	}
	return c.store(key, load(key))
}

// cachedSDE is a read-through memo over an SDE for one SDE generation. It
// satisfies SDE, so the interpreter and the post-resolution formula steps use it
// unchanged.
//
// Ownership: GetDogma returns a private copy on every call (callers mutate the
// map: the interpreter rewrites an item's attributes in place). Every other
// accessor returns shared, read-only data — GetGroupID/GetCategoryID pointers,
// GetTypeEffectIDs, GetEffectModifiers and GetSkillTypeIDs slices — which every
// caller in this package only reads.
//
// A read error surfaces from the SDE as an empty result and is indistinguishable
// from "no data", so an empty dogma map is never memoized (a hull the SDE failed
// to read must not stay broken); the other empty results are, which is safe
// because the SDE file is read-only and a failed read stays failed only until the
// next generation.
type cachedSDE struct {
	src SDE
	gen uint64

	dogma       memo[map[int]float64]
	groupID     memo[*int]
	categoryID  memo[*int]
	typeEffects memo[[]int]
	effectMods  memo[[]sde.Modifier]
	effectCat   memo[int]
	attrMeta    memo[sde.AttrMeta]
	skillIDs    atomic.Pointer[[]int]
}

var _ SDE = (*cachedSDE)(nil)

func newCachedSDE(src SDE, gen uint64) *cachedSDE { return &cachedSDE{src: src, gen: gen} }

func (c *cachedSDE) GetDogma(typeID int) map[int]float64 {
	if m, ok := c.dogma.lookup(typeID); ok {
		return maps.Clone(m)
	}
	m := c.src.GetDogma(typeID)
	if len(m) == 0 {
		return m // unknown type or failed read: do not memoize
	}
	c.dogma.store(typeID, maps.Clone(m)) // the cache keeps its own copy; m is the caller's
	return m
}

func (c *cachedSDE) GetGroupID(typeID int) *int { return c.groupID.get(typeID, c.src.GetGroupID) }
func (c *cachedSDE) GetCategoryID(typeID int) *int {
	return c.categoryID.get(typeID, c.src.GetCategoryID)
}
func (c *cachedSDE) GetTypeEffectIDs(typeID int) []int {
	return c.typeEffects.get(typeID, c.src.GetTypeEffectIDs)
}
func (c *cachedSDE) GetEffectModifiers(effectID int) []sde.Modifier {
	return c.effectMods.get(effectID, c.src.GetEffectModifiers)
}
func (c *cachedSDE) GetEffectCategory(effectID int) int {
	return c.effectCat.get(effectID, c.src.GetEffectCategory)
}
func (c *cachedSDE) GetAttributeMeta(attrID int) sde.AttrMeta {
	return c.attrMeta.get(attrID, c.src.GetAttributeMeta)
}

func (c *cachedSDE) GetSkillTypeIDs() []int {
	if p := c.skillIDs.Load(); p != nil {
		return *p
	}
	ids := c.src.GetSkillTypeIDs()
	if len(ids) > 0 { // nil from a stub or a failed read: ask again next time
		c.skillIDs.CompareAndSwap(nil, &ids)
	}
	return ids
}

// sdeCaches holds the current-generation cachedSDE of one Engine. It is a
// pointer field of Engine so value copies (WithDefaultAmmo) share one cache.
type sdeCaches struct {
	cur atomic.Pointer[cachedSDE]
}

// view returns the cachedSDE for src's current generation, replacing it when the
// SDE file was hot-reloaded since the last call. A Stats call takes one view and
// uses it throughout, so it reads one consistent snapshot even if a reload lands
// mid-call; it never installs an older generation over a newer one.
func (h *sdeCaches) view(src SDE) SDE {
	var gen uint64
	if g, ok := src.(generationer); ok {
		gen = g.Generation()
	}
	for {
		c := h.cur.Load()
		if c != nil && c.gen >= gen {
			return c
		}
		n := newCachedSDE(src, gen)
		if h.cur.CompareAndSwap(c, n) {
			return n
		}
	}
}
