package gofa

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
)

// The SDE subset (E7) must give the engine exactly what the SQLite SDE gives it. Needs the
// real SDE (EVE_CORE_SDE_PATH), like the other real-SDE tests.
func TestSubset_EngineMatchesSQLite(t *testing.T) {
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })

	sub, err := s.ExportSubset("test")
	require.NoError(t, err)

	// Size: the measured subset is 7,477 published types of the seven categories; the
	// byte sizes are logged, not asserted (they move with every SDE build).
	require.Len(t, sub.Types, 7477, "published types of categories 6,7,8,16,18,20,32 (analysis 19 engine-gap, measured)")
	var raw bytes.Buffer
	require.NoError(t, sub.WriteJSON(&raw))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	require.NoError(t, zw.Close())
	t.Logf("subset: %d types, %d effects, %d attributes, %.2f MB json, %.2f MB gzip", len(sub.Types), len(sub.Effects), len(sub.Attributes), float64(raw.Len())/1e6, float64(gz.Len())/1e6)
	require.Less(t, raw.Len(), 16<<20, "the host data-pack cap is 16 MiB")

	store, err := dogma.ReadSubset(bytes.NewReader(raw.Bytes()))
	require.NoError(t, err)

	f, unresolved := fit.ParseEFT(hawkEFT, s)
	require.Empty(t, unresolved)
	fs, unresolved := fit.ParseEFT(hawkEFT, store)
	require.Empty(t, unresolved)
	require.Equal(t, f, fs, "the same EFT parses to the same fit through both")

	want, err := New(s).Stats(t.Context(), f, fit.StatsOpts{})
	require.NoError(t, err)
	got, err := New(store).Stats(t.Context(), fs, fit.StatsOpts{})
	require.NoError(t, err)
	require.Equal(t, want, got, "subset engine == SQLite engine")

	// Validation agrees too.
	wr := fit.Validate(t.Context(), s, f, nil, false)
	gr := fit.Validate(t.Context(), store, fs, nil, false)
	require.Equal(t, wr, gr)
}

func TestSubset_RejectsOtherFormat(t *testing.T) {
	_, err := dogma.ReadSubset(bytes.NewBufferString(`{"format":99}`))
	require.Error(t, err)
}

// TestSubset_StartAndStatsTime logs (does not assert) how long the engine takes to start
// from the JSON subset and to evaluate a fit cold and warm; the L1 report quotes the numbers.
func TestSubset_StartAndStatsTime(t *testing.T) {
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	var raw bytes.Buffer
	require.NoError(t, sub.WriteJSON(&raw))

	t0 := time.Now()
	store, err := dogma.ReadSubset(bytes.NewReader(raw.Bytes()))
	require.NoError(t, err)
	load := time.Since(t0)
	f, _ := fit.ParseEFT(hawkEFT, store)
	eng := New(store)
	t1 := time.Now()
	_, err = eng.Stats(t.Context(), f, fit.StatsOpts{})
	require.NoError(t, err)
	cold := time.Since(t1)
	var warm time.Duration
	const n = 20
	t2 := time.Now()
	for i := 0; i < n; i++ {
		_, _ = eng.Stats(t.Context(), f, fit.StatsOpts{})
	}
	warm = time.Since(t2) / n
	t.Logf("subset load %v, first Stats %v, warm Stats %v (mean of %d)", load, cold, warm, n)
}
