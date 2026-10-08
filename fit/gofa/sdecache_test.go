package gofa

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
	"github.com/stretchr/testify/require"
)

// countingSDE wraps a fakeSDE, counts every read per method and exposes a
// settable generation like a hot-reloading *sde.SDE.
type countingSDE struct {
	*fakeSDE
	mu    sync.Mutex
	calls map[string]int
	gen   atomic.Uint64
}

func newCountingSDE(f *fakeSDE) *countingSDE {
	c := &countingSDE{fakeSDE: f, calls: map[string]int{}}
	c.gen.Store(1)
	return c
}

func (c *countingSDE) hit(name string) {
	c.mu.Lock()
	c.calls[name]++
	c.mu.Unlock()
}

// count returns how many times a method was read; "" sums every method.
func (c *countingSDE) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name != "" {
		return c.calls[name]
	}
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (c *countingSDE) Generation() uint64 { return c.gen.Load() }

func (c *countingSDE) GetDogma(id int) map[int]float64 {
	c.hit("GetDogma")
	return c.fakeSDE.GetDogma(id)
}
func (c *countingSDE) GetGroupID(id int) *int { c.hit("GetGroupID"); return c.fakeSDE.GetGroupID(id) }
func (c *countingSDE) GetCategoryID(id int) *int {
	c.hit("GetCategoryID")
	return c.fakeSDE.GetCategoryID(id)
}
func (c *countingSDE) GetTypeEffectIDs(id int) []int {
	c.hit("GetTypeEffectIDs")
	return c.fakeSDE.GetTypeEffectIDs(id)
}
func (c *countingSDE) GetEffectModifiers(id int) []sde.Modifier {
	c.hit("GetEffectModifiers")
	return c.fakeSDE.GetEffectModifiers(id)
}
func (c *countingSDE) GetEffectCategory(id int) int {
	c.hit("GetEffectCategory")
	return c.fakeSDE.GetEffectCategory(id)
}
func (c *countingSDE) GetAttributeMeta(id int) sde.AttrMeta {
	c.hit("GetAttributeMeta")
	return c.fakeSDE.GetAttributeMeta(id)
}
func (c *countingSDE) GetSkillTypeIDs() []int { c.hit("GetSkillTypeIDs"); return []int{900, 901, 902} }

// cacheTestSDE: hull 100, one module 200 boosting hull attr 40, three skills.
func cacheTestSDE() *countingSDE {
	return newCountingSDE(&fakeSDE{
		dogma: map[int]map[int]float64{
			100: {40: 1000},
			200: {50: 10},
			900: {1: 1}, 901: {1: 2}, 902: {1: 3},
		},
		groupID: map[int]int{200: 55},
		effects: map[int][]int{200: {99}},
		modifiers: map[int][]sde.Modifier{99: {{
			Domain: "shipID", Func: "ItemModifier", ModifiedAttr: 40, ModifyingAttr: 50, Operation: opPostPercent,
		}}},
		attrMeta: map[int]sde.AttrMeta{40: {Stackable: true}},
		category: map[int]int{99: 4},
	})
}

func cacheTestFit() fit.Fit {
	return fit.Fit{HullID: 100, Mid: []fit.FitModule{{TypeID: 200, Qty: 1}}}
}

func TestCachedSDE_MemoizesEveryRead(t *testing.T) {
	src := cacheTestSDE()
	c := newCachedSDE(src, 1)

	for i := 0; i < 3; i++ {
		require.Equal(t, map[int]float64{50: 10}, c.GetDogma(200))
		require.Equal(t, 55, *c.GetGroupID(200))
		require.Nil(t, c.GetGroupID(404), "an unknown type is memoized as nil too")
		require.Nil(t, c.GetCategoryID(200))
		require.Equal(t, []int{99}, c.GetTypeEffectIDs(200))
		require.Len(t, c.GetEffectModifiers(99), 1)
		require.Equal(t, 4, c.GetEffectCategory(99))
		require.True(t, c.GetAttributeMeta(40).Stackable)
		require.Equal(t, []int{900, 901, 902}, c.GetSkillTypeIDs())
	}

	for _, m := range []string{"GetDogma", "GetGroupID", "GetCategoryID", "GetTypeEffectIDs",
		"GetEffectModifiers", "GetEffectCategory", "GetAttributeMeta", "GetSkillTypeIDs"} {
		want := 1
		if m == "GetGroupID" {
			want = 2 // typeID 200 and the unknown 404
		}
		require.Equal(t, want, src.count(m), "%s must reach the SDE once per distinct key", m)
	}
}

// GetDogma hands out maps the interpreter rewrites in place: the cache must give
// every caller its own copy and never alias its master.
func TestCachedSDE_GetDogmaReturnsPrivateCopies(t *testing.T) {
	src := cacheTestSDE()
	c := newCachedSDE(src, 1)

	first := c.GetDogma(200)
	first[50] = -1
	first[999] = 7

	require.Equal(t, map[int]float64{50: 10}, c.GetDogma(200), "mutating a returned map leaked into the cache")
	require.Equal(t, map[int]float64{50: 10}, c.GetDogma(200))
}

// An empty dogma map is what the SDE returns for an unknown type AND for a failed
// read; memoizing it would keep a type broken until the next SDE generation.
func TestCachedSDE_DoesNotMemoizeEmptyDogma(t *testing.T) {
	src := cacheTestSDE()
	c := newCachedSDE(src, 1)

	require.Empty(t, c.GetDogma(777))
	require.Empty(t, c.GetDogma(777))
	require.Equal(t, 2, src.count("GetDogma"), "an empty result must be re-read")

	src.fakeSDE.dogma[777] = map[int]float64{1: 2} // the data shows up later
	require.Equal(t, map[int]float64{1: 2}, c.GetDogma(777))
	require.Equal(t, map[int]float64{1: 2}, c.GetDogma(777))
	require.Equal(t, 3, src.count("GetDogma"), "the first non-empty result is memoized")
}

func TestMemo_StopsStoringAtTheCap(t *testing.T) {
	var m memo[int]
	loads := 0
	load := func(k int) int { loads++; return k * 2 }

	for k := 0; k < maxMemoEntries+50; k++ {
		require.Equal(t, k*2, m.get(k, load))
	}
	require.EqualValues(t, maxMemoEntries, m.n.Load(), "memo must not grow past the cap")

	before := loads
	require.Equal(t, 2*(maxMemoEntries+10), m.get(maxMemoEntries+10, load), "over-cap keys still resolve")
	require.Equal(t, before+1, loads, "over-cap keys are re-read, not stored")
	require.Equal(t, 6, m.get(3, load))
	require.Equal(t, before+1, loads, "stored keys keep being served from memory")
}

func TestSDECaches_ViewFollowsGeneration(t *testing.T) {
	src := cacheTestSDE()
	var h sdeCaches

	v1 := h.view(src)
	require.Same(t, v1, h.view(src), "same generation → same cache")

	src.gen.Store(2) // SDE file replaced and hot-reloaded
	v2 := h.view(src)
	require.NotSame(t, v1, v2, "a new generation must start a fresh cache")
	require.Same(t, v2, h.view(src))

	src.gen.Store(1) // a caller that read the generation before the reload
	require.Same(t, v2, h.view(src), "an older generation must never replace a newer cache")
}

func TestSDECaches_SDEWithoutGenerationIsStatic(t *testing.T) {
	var h sdeCaches
	s := &fakeSDE{dogma: map[int]map[int]float64{1: {1: 1}}}
	require.Same(t, h.view(s), h.view(s))
}

// After the first Stats call the whole fit is served from memory: a second call
// reads nothing from the SDE (only the cheap generation check remains).
func TestEngine_StatsReadsSDEOnce(t *testing.T) {
	src := cacheTestSDE()
	eng := New(src)
	ctx := context.Background()

	first, err := eng.Stats(ctx, cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	warm := src.count("")
	require.Positive(t, warm)

	second, err := eng.Stats(ctx, cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, warm, src.count(""), "a warm Stats call must not read the SDE")
}

// A WithDefaultAmmo copy shares the cache of the Engine it came from.
func TestEngine_WithDefaultAmmoSharesCache(t *testing.T) {
	src := cacheTestSDE()
	eng := New(src)
	_, err := eng.Stats(context.Background(), cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	warm := src.count("")

	_, err = eng.WithDefaultAmmo().Stats(context.Background(), cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	require.Equal(t, warm, src.count(""))
}

// The cache must not outlive the SDE build it was filled from: after a hot reload
// (generation bump) the next Stats call sees the new data.
func TestEngine_StatsFollowsSDEReload(t *testing.T) {
	src := cacheTestSDE()
	eng := New(src)

	ship, _, err := eng.resolve(cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	require.InDelta(t, 1100.0, ship.Attrs[40], 1e-9)

	src.fakeSDE.dogma[200] = map[int]float64{50: 20} // monthly SDE rebuild changes the module
	ship, _, err = eng.resolve(cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	require.InDelta(t, 1100.0, ship.Attrs[40], 1e-9, "same generation: static data is served from the cache")

	src.gen.Store(2) // the SDE handle reloaded
	ship, _, err = eng.resolve(cacheTestFit(), fit.StatsOpts{})
	require.NoError(t, err)
	require.InDelta(t, 1200.0, ship.Attrs[40], 1e-9, "new generation must re-read the SDE")
}

// An Engine built without New (no cache) still works against the raw SDE.
func TestEngine_WithoutCacheReadsSDEDirectly(t *testing.T) {
	src := cacheTestSDE()
	eng := &Engine{sde: src}

	for i := 0; i < 2; i++ {
		ship, _, err := eng.resolve(cacheTestFit(), fit.StatsOpts{})
		require.NoError(t, err)
		require.InDelta(t, 1100.0, ship.Attrs[40], 1e-9)
	}
	require.Equal(t, 2, src.count("GetSkillTypeIDs"), "no cache: every resolve reads the SDE")
}

// Concurrent Stats calls while the SDE generation churns: run with -race. Every
// call must succeed and compute the same, correct hull attribute.
func TestEngine_ConcurrentStatsWithGenerationChurn(t *testing.T) {
	src := cacheTestSDE()
	eng := New(src)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if i%10 == w { // some goroutines bump the generation mid-flight
					src.gen.Add(1)
				}
				ship, _, err := eng.resolve(cacheTestFit(), fit.StatsOpts{})
				if err != nil {
					errs <- err
					return
				}
				if got := ship.Attrs[40]; got < 1099.999999 || got > 1100.000001 {
					errs <- fmt.Errorf("hull attr 40 = %v, want 1100", got)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
