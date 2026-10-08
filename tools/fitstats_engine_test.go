package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// corpusEFT returns one named fit of the shared gofa corpus (testing.TB, so the
// benchmarks can use it too).
func corpusEFT(tb testing.TB, name string) string {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "gofa", "corpus.json"))
	require.NoError(tb, err)
	var corpus []struct {
		Name string `json:"name"`
		EFT  string `json:"eft"`
	}
	require.NoError(tb, json.Unmarshal(raw, &corpus))
	for _, c := range corpus {
		if c.Name == name {
			return c.EFT
		}
	}
	tb.Fatalf("corpus fit %q not found", name)
	return ""
}

// countingSDE counts the dogma reads the engine makes against the real SDE: a
// warm engine serves them from its memo, a cold one goes to SQLite again. The
// embedded *sde.SDE promotes the name lookups ApplyDefaultAmmo needs.
type countingSDE struct {
	*sde.SDE
	dogma atomic.Int64
}

func (c *countingSDE) GetDogma(typeID int) map[int]float64 {
	c.dogma.Add(1)
	return c.SDE.GetDogma(typeID)
}

// TestStatsEngine_LazyFallbackIsOneSharedEngine: a Deps built as a struct literal
// (tests, tooling) lazily builds one engine, every call and goroutine gets the
// same one, and an injected engine wins over the fallback. Runs without the real
// SDE, and under -race.
func TestStatsEngine_LazyFallbackIsOneSharedEngine(t *testing.T) {
	require.Nil(t, (&Deps{}).StatsEngine(), "no SDE, no engine")
	require.Nil(t, (*Deps)(nil).StatsEngine(), "a nil Deps has no engine")

	d := &Deps{SDE: &sde.SDE{}}
	const workers = 32
	got := make([]*gofa.Engine, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() { got[i] = d.StatsEngine() })
	}
	wg.Wait()
	require.NotNil(t, got[0])
	for i, e := range got {
		require.Same(t, got[0], e, "worker %d got a different engine", i)
	}
	require.Same(t, got[0], d.StatsEngine(), "later calls reuse it")

	injected := gofa.New(&sde.SDE{})
	d = &Deps{SDE: &sde.SDE{}, Stats: injected}
	require.Same(t, injected, d.StatsEngine(), "an injected engine is used as is")
}

// TestComputeFitStats_ReusesOneWarmEngine: the second call for a fit runs on the
// engine's SDE memo (no new dogma reads) and prints the same card as the first,
// which is the byte-identical contract of sharing the engine.
func TestComputeFitStats_ReusesOneWarmEngine(t *testing.T) {
	deps := testDeps(t)
	cs := &countingSDE{SDE: deps.SDE}
	deps.Stats = gofa.New(cs)
	args := map[string]any{"eft_text": corpusEFT(t, "caracal-missile")}

	first, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", args)
	require.NoError(t, err)
	cold := cs.dogma.Load()
	require.Positive(t, cold, "the injected engine must serve the call (cold reads hit the SDE)")

	second, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", args)
	require.NoError(t, err)
	require.Equal(t, cold, cs.dogma.Load(), "the second call must be served from the warm memo")
	require.Equal(t, first.Text, second.Text)
	require.Equal(t, first.Data, second.Data)
}

// TestValidateFitting_StatCardReusesTheSameEngine: the validate_fitting stat card
// runs on the engine compute_fit_stats warmed, not on an engine of its own.
func TestValidateFitting_StatCardReusesTheSameEngine(t *testing.T) {
	deps := testDeps(t)
	cs := &countingSDE{SDE: deps.SDE}
	deps.Stats = gofa.New(cs)

	_, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", map[string]any{"eft_text": validRifterEFT})
	require.NoError(t, err)
	warm := cs.dogma.Load()
	require.Positive(t, warm)

	got, err := ExecuteTool(testCtx(t), deps, "validate_fitting", map[string]any{"eft_text": validRifterEFT})
	require.NoError(t, err)
	require.Contains(t, got, "Fit stats: Rifter (all-V)")
	require.Equal(t, warm, cs.dogma.Load(), "the stat card must be served from the shared warm engine")
}

// TestComputeFitStats_ConcurrentCallsShareTheEngine: concurrent calls on one Deps
// (the chat loop, the public tool API and the guards all share it) are race-free
// and agree with each other. Lazy fallback path: Stats is left nil on purpose.
func TestComputeFitStats_ConcurrentCallsShareTheEngine(t *testing.T) {
	deps := testDeps(t)
	eft := corpusEFT(t, "rifter-turret") // a frigate: the cheapest fit under -race
	want, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", map[string]any{"eft_text": eft})
	require.NoError(t, err)

	const workers = 8
	texts := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			res, err := ExecuteToolResult(testCtx(t), deps, "compute_fit_stats", map[string]any{"eft_text": eft})
			texts[i], errs[i] = res.Text, err
		})
	}
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
		require.Equal(t, want.Text, texts[i], "worker %d", i)
	}
}

// BenchmarkComputeFitStats_RepeatedCalls is the latency of compute_fit_stats on
// one Deps called over and over, the production shape (one Deps per process).
func BenchmarkComputeFitStats_RepeatedCalls(b *testing.B) {
	for _, name := range []string{"caracal-missile", "rifter-turret"} {
		b.Run(name, func(b *testing.B) {
			s, err := sde.Open(sdetest.Path(b))
			require.NoError(b, err)
			b.Cleanup(func() { s.Close() })
			deps := &Deps{SDE: s, Client: NewClient()}
			args := map[string]any{"eft_text": corpusEFT(b, name)}

			b.ReportAllocs()
			for b.Loop() {
				res, err := ExecuteToolResult(b.Context(), deps, "compute_fit_stats", args)
				if err != nil || res.Text == "" {
					b.Fatalf("compute_fit_stats: %v %q", err, res.Text)
				}
			}
		})
	}
}
