package gofa

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dogma"
	"eve-cyno.dev/go/data/fit"
	realsde "eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
)

// The SDE subset (E7) must give the engine exactly what the SQLite SDE gives it. Needs the
// real SDE (EVE_CORE_SDE_PATH), like the other real-SDE tests.
func TestSubset_EngineMatchesSQLite(t *testing.T) {
	skipHeavyRace(t)
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })

	// The expected count comes from the SQLite SDE itself, read independently of the
	// exporter, because it moves with every SDE build.
	path := sdetest.Path(t)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cats := make([]string, len(realsde.SubsetCategories))
	for i, c := range realsde.SubsetCategories {
		cats[i] = strconv.Itoa(c)
	}
	catList := strings.Join(cats, ",")
	var want int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM invTypes t JOIN invGroups g ON g.groupID = t.groupID
		WHERE g.categoryID IN (`+catList+`) AND t.published = 1`).Scan(&want))
	t.Logf("expected published in-scope types: %d (from %s)", want, path)

	sub, err := s.ExportSubset("test")
	require.NoError(t, err)

	// Size: the published types of the seven categories must equal the count read from the
	// SQLite SDE above; the byte sizes are logged, not asserted (they move with every SDE build).
	scoped := 0
	inScope := map[int]bool{}
	for _, c := range realsde.SubsetCategories {
		inScope[c] = true
	}
	for _, ty := range sub.Types {
		if inScope[ty.Category] && !ty.Unpublished {
			scoped++
		}
	}
	require.Equal(t, want, scoped, "published types of categories "+catList+" in the subset match the SQLite SDE")
	require.Greater(t, len(sub.Types), scoped, "unpublished in-scope types and name-only entries come on top")
	var all int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM invTypes`).Scan(&all))
	require.Equal(t, all, len(sub.Types), "every type ResolveNames can resolve is in the subset")
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
		// Chalcopyrite (27029) is an unpublished mineral, outside the engine's categories:
		// the SQLite SDE resolves it, so the subset must too (SV-M13).
		"unpublished cargo": "[Rifter, cargo]\n\nChalcopyrite x10",
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
	skipHeavyRace(t)
	s := openRealSDE(t)
	t.Cleanup(func() { _ = s.Close() })
	sub, err := s.ExportSubset("test")
	require.NoError(t, err)
	// Reverse the effects so WriteJSON has real sorting to do; the Store is built after, so
	// it indexes the shuffled slice. A WriteJSON that sorted in place would move the
	// effects under the Store's pointers.
	for i, j := 0, len(sub.Effects)-1; i < j; i, j = i+1, j-1 {
		sub.Effects[i], sub.Effects[j] = sub.Effects[j], sub.Effects[i]
	}
	store := dogma.NewStore(sub)
	want := map[int]int{}
	for _, e := range sub.Effects {
		want[e.ID] = store.GetEffectCategory(e.ID)
	}
	var buf bytes.Buffer
	require.NoError(t, sub.WriteJSON(&buf))
	for id, cat := range want {
		require.Equal(t, cat, store.GetEffectCategory(id), "WriteJSON must not reorder what the Store indexes (effect %d)", id)
	}
	var out struct{ Effects []struct{ ID int } }
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	require.True(t, sort.SliceIsSorted(out.Effects, func(i, j int) bool { return out.Effects[i].ID < out.Effects[j].ID }), "WriteJSON writes effects sorted by id")
	back, err := dogma.ReadSubset(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	for id, cat := range want {
		require.Equal(t, cat, back.GetEffectCategory(id), "the written subset keeps every effect (effect %d)", id)
	}
}

func TestSubset_RejectsOtherFormat(t *testing.T) {
	_, err := dogma.ReadSubset(bytes.NewBufferString(`{"format":99}`))
	require.Error(t, err)
}

// TestSubset_StartAndStatsTime logs (does not assert) how long the engine takes to start
// from the JSON subset and to evaluate a fit cold and warm; the L1 report quotes the numbers.
func TestSubset_StartAndStatsTime(t *testing.T) {
	skipHeavyRace(t)
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
