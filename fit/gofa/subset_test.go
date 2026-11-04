package gofa

import (
	"bytes"
	"compress/gzip"
	"strings"
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
	scoped := 0
	inScope := map[int]bool{6: true, 7: true, 8: true, 16: true, 18: true, 20: true, 32: true}
	for _, ty := range sub.Types {
		if inScope[ty.Category] && !ty.Unpublished {
			scoped++
		}
	}
	require.Equal(t, 7477, scoped, "published types of categories 6,7,8,16,18,20,32 (analysis 19 engine-gap, measured)")
	require.Greater(t, len(sub.Types), scoped, "unpublished in-scope types and name-only entries come on top")
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

	for name, eft := range map[string]string{
		"hawk, no ammo":   hawkEFT,
		"hawk, charged":   strings.Replace(hawkEFT, "Rocket Launcher II\n", "Rocket Launcher II, Scourge Rocket\n", -1),
		"cargo and boost": hawkEFT + "\n\n\nLiquid Ozone x100\nSynth Blue Pill Booster\nStrontium Clathrates x50",
		"unpublished":     "[Rifter, legacy]\n\nEmergency Damage Control I\nSupplemental Scanning CPU I",
	} {
		t.Run(name, func(t *testing.T) {
			f, unresolved := fit.ParseEFT(eft, s)
			fs, unresolvedS := fit.ParseEFT(eft, store)
			require.Equal(t, unresolved, unresolvedS, "the same names resolve through both")
			require.Equal(t, f.Cargo, fs.Cargo)
			require.Equal(t, f.HullID, fs.HullID)
			require.Equal(t, fit.Hash(f), fit.Hash(fs))

			for _, defAmmo := range []bool{false, true} {
				we, ge := New(s), New(store)
				if defAmmo {
					we, ge = we.WithDefaultAmmo(), ge.WithDefaultAmmo()
				}
				want, err := we.Stats(t.Context(), f, fit.StatsOpts{})
				require.NoError(t, err)
				got, err := ge.Stats(t.Context(), fs, fit.StatsOpts{})
				require.NoError(t, err)
				require.Equal(t, want, got, "subset engine == SQLite engine (default ammo %v)", defAmmo)
			}
			require.Equal(t, fit.Validate(t.Context(), s, f, nil, false), fit.Validate(t.Context(), store, fs, nil, false))
		})
	}
}

func TestSubset_StoreSurvivesWriteJSON(t *testing.T) {
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	store := dogma.NewStore(sub)
	before := store.GetEffectCategory(sub.Effects[len(sub.Effects)/2].ID)
	id := sub.Effects[len(sub.Effects)/2].ID
	var buf bytes.Buffer
	require.NoError(t, sub.WriteJSON(&buf))
	require.Equal(t, before, store.GetEffectCategory(id), "WriteJSON must not reorder what the Store indexes")
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
