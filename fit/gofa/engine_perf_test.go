package gofa

// engine_perf_test.go — latency benchmark and result-stability tests for
// Engine.Stats against the real SDE (skipped when data/sde/sde.sqlite is absent).
//
// Run the benchmark:
//
//	go -C core test ./fit/gofa/ -run X -bench BenchmarkStats -benchmem
//
// The equality tests pin the contract of the SDE read cache: caching must never
// change a number, so Stats on a warm Engine, on a second call and on a fresh
// Engine must be deep-equal.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// perfFit is one named real fit parsed against the SDE.
type perfFit struct {
	name string
	fit  fit.Fit
}

// perfCorpusEntry mirrors one row of core/testdata/golden/gofa/corpus.json.
type perfCorpusEntry struct {
	Name string `json:"name"`
	EFT  string `json:"eft"`
}

// loadPerfFits parses the shared gofa corpus and the QC2 fit slice into Fits.
// When only is non-empty just those names are returned (corpus names and
// "qc2/<file>.eft" names).
func loadPerfFits(tb testing.TB, s *realsde.SDE, only ...string) []perfFit {
	tb.Helper()
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	keep := func(n string) bool { return len(want) == 0 || want[n] }

	var out []perfFit
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "gofa", "corpus.json"))
	require.NoError(tb, err, "read corpus.json")
	var corpus []perfCorpusEntry
	require.NoError(tb, json.Unmarshal(raw, &corpus), "parse corpus.json")
	for _, c := range corpus {
		if !keep(c.Name) {
			continue
		}
		f, _ := fit.ParseEFT(c.EFT, s)
		out = append(out, perfFit{name: c.Name, fit: f})
	}

	files, err := filepath.Glob(filepath.Join("..", "testdata", "qc2", "*.eft"))
	require.NoError(tb, err)
	sort.Strings(files)
	for _, p := range files {
		name := "qc2/" + filepath.Base(p)
		if !keep(name) {
			continue
		}
		eft, err := os.ReadFile(p)
		require.NoError(tb, err, "read %s", p)
		f, _ := fit.ParseEFT(string(eft), s)
		out = append(out, perfFit{name: name, fit: f})
	}
	require.NotEmpty(tb, out, "no fits loaded")
	return out
}

// BenchmarkStats measures Engine.Stats on three real fits (turret + drone hull,
// a missile hull and a QC2 capital-sized fit). "warm" reuses one Engine across
// iterations (production: one Engine per process); "cold" builds a fresh Engine
// each iteration, so every SDE read cache starts empty.
func BenchmarkStats(b *testing.B) {
	s := openBenchSDE(b)
	defer s.Close()
	fits := loadPerfFits(b, s, "vexor-drone", "caracal-missile", "qc2/q182_vargur.eft")
	ctx := context.Background()

	b.Run("warm", func(b *testing.B) {
		eng := New(s)
		for _, pf := range fits { // prime outside the timer
			_, err := eng.Stats(ctx, pf.fit, fit.StatsOpts{})
			require.NoError(b, err)
		}
		for _, pf := range fits {
			b.Run(pf.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := eng.Stats(ctx, pf.fit, fit.StatsOpts{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	})

	b.Run("cold", func(b *testing.B) {
		for _, pf := range fits {
			b.Run(pf.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := New(s).Stats(ctx, pf.fit, fit.StatsOpts{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	})
}

// openBenchSDE is openRealSDE for testing.TB (benchmarks cannot take *testing.T).
func openBenchSDE(tb testing.TB) *realsde.SDE {
	tb.Helper()
	s, err := realsde.Open(sdetest.Path(tb))
	if err != nil || !s.Available() {
		sdetest.Skip(tb, "data/sde/sde.sqlite not available; skipping real-SDE test")
	}
	return s
}

// determinismFits is the fit slice the equality tests run on: every weapon
// family (turret, missile + BCS, drone, sentry, T3 subsystems) and two QC2 fits.
var determinismFits = []string{
	"rifter-turret", "caracal-missile", "vexor-drone", "ishtar-sentry", "tengu-t3",
	"raven-cruise", "qc2/q182_vargur.eft", "qc2/q200_cerberus.eft",
}

// TestStats_DeterministicAcrossCallsAndEngines pins "caching changes no number":
// the same fit must produce a deep-equal FitStats on the first call, on a second
// call of the same Engine (warm caches) and on a brand-new Engine (cold caches),
// with the all-V default and with a partial skill profile.
//
// It also guards a pre-existing flaw the cache work fixed: skills used to enter
// the interpreter in random map order, so unpenalized bonuses were multiplied in
// a different order each call and the last bit of the result (1 ulp) flipped.
func TestStats_DeterministicAcrossCallsAndEngines(t *testing.T) {
	s := openBenchSDE(t)
	defer s.Close()
	fits := loadPerfFits(t, s, determinismFits...)
	ctx := context.Background()

	partial := fit.StatsOpts{Skills: fit.SkillProfile{3300: 3, 3319: 1, 3426: 0}} // overrides the all-V default
	shared := New(s)
	for _, opts := range []fit.StatsOpts{{}, partial} {
		for _, pf := range fits {
			first, err := shared.Stats(ctx, pf.fit, opts)
			require.NoError(t, err, pf.name)
			second, err := shared.Stats(ctx, pf.fit, opts)
			require.NoError(t, err, pf.name)
			require.Equal(t, first, second, "%s: second call on the same Engine differs", pf.name)
		}
	}
	for _, pf := range fits {
		want, err := shared.Stats(ctx, pf.fit, fit.StatsOpts{})
		require.NoError(t, err, pf.name)
		fresh, err := New(s).Stats(ctx, pf.fit, fit.StatsOpts{}) // cold caches
		require.NoError(t, err, pf.name)
		require.Equal(t, want, fresh, "%s: fresh Engine differs", pf.name)
	}
}

// TestStats_CachedEngineMatchesUncachedEngine is the "caching changes no number"
// proof: an Engine built without the read cache (every lookup goes to SQLite, the
// pre-cache behaviour) must produce exactly the same FitStats as New. The raw
// path takes ~1.3 s per fit, so this checks three fits that exercise turrets,
// missiles/BCS and drones.
func TestStats_CachedEngineMatchesUncachedEngine(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("raw SDE path takes ~1.3 s per fit (~35 s under -race)")
	}
	s := openBenchSDE(t)
	defer s.Close()
	ctx := context.Background()
	cached := New(s)
	raw := &Engine{sde: s} // no read cache

	for _, pf := range loadPerfFits(t, s, "rifter-turret", "caracal-missile", "vexor-drone") {
		for _, opts := range []fit.StatsOpts{{}, {Skills: fit.SkillProfile{3300: 3, 3319: 1, 3426: 0}}} {
			want, err := raw.Stats(ctx, pf.fit, opts)
			require.NoError(t, err, pf.name)
			got, err := cached.Stats(ctx, pf.fit, opts)
			require.NoError(t, err, pf.name)
			require.Equal(t, want, got, "%s: read cache changed the result", pf.name)
		}
	}
}

// TestStats_SkillProfileNotLeakedAcrossCalls guards the shared all-V skill base:
// a call with a reduced skill profile must not leak into the next default call
// (a mutated cached skill map would silently degrade every later fit).
func TestStats_SkillProfileNotLeakedAcrossCalls(t *testing.T) {
	s := openBenchSDE(t)
	defer s.Close()
	pf := loadPerfFits(t, s, "caracal-missile")[0]
	ctx := context.Background()
	eng := New(s)

	before, err := eng.Stats(ctx, pf.fit, fit.StatsOpts{})
	require.NoError(t, err)
	// Level 0 for every skill the fit could use: a clearly different result.
	zero := fit.SkillProfile{}
	for _, id := range s.GetSkillTypeIDs() {
		zero[id] = 0
	}
	low, err := eng.Stats(ctx, pf.fit, fit.StatsOpts{Skills: zero})
	require.NoError(t, err)
	require.NotEqual(t, before, low, "an untrained character must change the stats")
	after, err := eng.Stats(ctx, pf.fit, fit.StatsOpts{})
	require.NoError(t, err)
	require.Equal(t, before, after, "a reduced skill profile leaked into a later default call")
}

// TestStats_ConcurrentCallsAreSafeAndEqual hammers one Engine from several
// goroutines (core-api serves concurrent turns). Run with -race; every result
// must equal the single-threaded reference.
func TestStats_ConcurrentCallsAreSafeAndEqual(t *testing.T) {
	s := openBenchSDE(t)
	defer s.Close()
	fits := loadPerfFits(t, s, "vexor-drone", "caracal-missile", "tengu-t3", "qc2/q182_vargur.eft")
	ctx := context.Background()

	want := make([]fit.FitStats, len(fits))
	ref := New(s)
	for i, pf := range fits {
		st, err := ref.Stats(ctx, pf.fit, fit.StatsOpts{})
		require.NoError(t, err, pf.name)
		want[i] = st
	}

	eng := New(s) // cold: the goroutines race to populate the caches
	const workers = 8
	errs := make(chan string, workers*len(fits))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for k := range fits {
				i := (k + w) % len(fits) // stagger so workers hit different fits first
				got, err := eng.Stats(ctx, fits[i].fit, fit.StatsOpts{})
				switch {
				case err != nil:
					errs <- fits[i].name + ": " + err.Error()
				case !equalStats(got, want[i]):
					errs <- fits[i].name + ": concurrent result differs from reference"
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	var msgs []string
	for m := range errs {
		msgs = append(msgs, m)
	}
	require.Empty(t, msgs, strings.Join(msgs, "; "))
}

// equalStats compares two FitStats structurally (goroutine-safe: no *testing.T).
func equalStats(a, b fit.FitStats) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
