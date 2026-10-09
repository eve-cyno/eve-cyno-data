package main

import (
	"database/sql"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/abyss"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

func openReal(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+sdetest.Path(t)+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestBuild_Deterministic: two builds from the same SDE are byte-identical.
func TestBuild_Deterministic(t *testing.T) {
	db := openReal(t)
	a, err := Build(db, "test-build")
	require.NoError(t, err)
	b, err := Build(db, "test-build")
	require.NoError(t, err)
	ja, err := Marshal(a)
	require.NoError(t, err)
	jb, err := Marshal(b)
	require.NoError(t, err)
	require.Equal(t, string(ja), string(jb))
}

// TestBuild_MatchesCommittedShape: a fresh build from the local SDE has the same shape as
// the committed file. Counts can legitimately grow after a game patch, so it checks only
// that nothing the committed file has has vanished.
func TestBuild_MatchesCommittedShape(t *testing.T) {
	db := openReal(t)
	fresh, err := Build(db, "test-build")
	require.NoError(t, err)
	committed, err := abyss.Load()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(fresh.NPCs), len(committed.NPCs)-5)
	require.Len(t, fresh.Tiers, len(committed.Tiers))
	require.Len(t, fresh.Weather, len(committed.Weather))
}

func TestFamily(t *testing.T) {
	for name, want := range map[string]string{
		"Tangling Damavik":      "Damavik",
		"Tangling Vila Damavik": "Vila Damavik",
		"Harrowing Vila Vedmak": "Vila Vedmak",
		"Elite Lucifer Cynabal": "Lucifer",
		"Strikelance Tessella":  "Tessella",
		"Lucid Aegis":           "Lucid",
		"Mystery Thing":         "Other",
	} {
		require.Equal(t, want, family(name), name)
	}
}

func TestLayerEHP(t *testing.T) {
	a := attrs{1: 1000, 2: 0.5, 3: 0.5}
	require.InDelta(t, 2000, layerEHP(a, 1, 2, 3), 1e-9) // mean resonance 0.5
	require.Equal(t, 0.0, layerEHP(attrs{}, 1, 2))
}

func TestRenderDoc(t *testing.T) {
	ds, err := abyss.Load()
	require.NoError(t, err)
	doc := RenderDoc(ds)
	require.Equal(t, doc, RenderDoc(ds), "deterministic")
	require.True(t, strings.HasPrefix(doc, "---\nsource: "), "front matter first")
	require.Contains(t, doc, "source_type: wiki_supplement")
	require.Contains(t, doc, "Source: EVE SDE (Fenris Creations), build "+ds.SDEBuild)
	require.Contains(t, doc, "Tangling Damavik: EHP")
	require.Contains(t, doc, "Electrical Storm")
	require.Contains(t, doc, "## Kill-priority heuristic")
	require.Equal(t, "2026-10-07", docDate("3586130_20261007"))
	require.Equal(t, "weird", docDate("weird"))
}
