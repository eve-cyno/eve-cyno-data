package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eve-cyno.dev/go/data/corpus"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// ─── repr helpers unit tests (no Qdrant needed) ───────────────────────────

func TestPyListRepr(t *testing.T) {
	require.Equal(t, "[]", pyListRepr(nil))
	require.Equal(t, "[]", pyListRepr([]string{}))
	require.Equal(t, "['abyss']", pyListRepr([]string{"abyss"}))
	require.Equal(t, "['abyss', 'filament']", pyListRepr([]string{"abyss", "filament"}))
	require.Equal(t, "['abyss', 'abyss-exotic', 'abyss-t3']", pyListRepr([]string{"abyss", "abyss-exotic", "abyss-t3"}))
}

func TestPyStrRepr(t *testing.T) {
	require.Equal(t, "'abyss'", pyRepr("abyss"))
	require.Equal(t, "'wormhole'", pyRepr("wormhole"))
}

func TestPyDictRepr(t *testing.T) {
	// single string value
	pairs := []struct{ k, v string }{{"ship_name", "Gila"}}
	require.Equal(t, "{'ship_name': 'Gila'}", pyDictRepr(pairs, nil))

	// multiple values
	pairs2 := []struct{ k, v string }{
		{"ship_name", "NoSuchShip999"},
	}
	require.Equal(t, "{'ship_name': 'NoSuchShip999'}", pyDictRepr(pairs2, nil))

	// bool value (alpha_clone: True)
	pairs3 := []struct{ k, v string }{
		{"ship_name", "Rifter"},
		{"alpha_clone", "True"},
	}
	boolKeys := map[string]bool{"alpha_clone": true}
	require.Equal(t, "{'ship_name': 'Rifter', 'alpha_clone': True}", pyDictRepr(pairs3, boolKeys))

	// empty
	require.Equal(t, "{}", pyDictRepr(nil, nil))
}

// ─── corpus-gated live tests ──────────────────────────────────────────────

// liveDepsOrSkip builds a *Deps with SDE + Retriever, skipping when either is unavailable.
// Does NOT use bootstrap (which imports tools → cycle). Mirrors bootstrap.BuildDeps logic.
func liveDepsOrSkip(t *testing.T) *Deps {
	t.Helper()
	dbPath := sdetest.Path(t)
	// get_fits scrolls Qdrant, so a dev machine with the SDE but without the
	// local stack must skip rather than fail.
	if conn, err := net.DialTimeout("tcp", "localhost:6333", 500*time.Millisecond); err != nil {
		t.Skipf("corpus-v0 unavailable: Qdrant not reachable at localhost:6333: %v", err)
	} else {
		_ = conn.Close()
	}
	s, err := sde.Open(dbPath)
	if err != nil {
		sdetest.Skipf(t, "sde open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	embed := rag.NewOllamaEmbedClient("http://localhost:11434", "nomic-embed-text")
	retr := rag.NewQdrantRetriever("http://localhost:6333", corpus.DefaultCollection, embed, 5)
	// best-effort LoadTitles
	_ = retr.LoadTitles(context.Background())

	return &Deps{SDE: s, Client: NewClient(), Retriever: retr}
}

func ctx(t *testing.T) context.Context { return context.Background() }

func golden(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "golden", rel))
	require.NoError(t, err, "golden file %s not found", rel)
	return string(b)
}

// TestGetFitsGilaSkeleton — structural assertions only; the exact bytes are not pinned.
func TestGetFitsGilaSkeleton(t *testing.T) {
	deps := liveDepsOrSkip(t)
	out, err := ExecuteTool(ctx(t), deps, "get_fits", map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Contains(t, out, "community fit(s) — copy each EFT block VERBATIM")
	require.Contains(t, out, "--- Fit 1/")
	require.Contains(t, out, "VERBATIM RULE:")
	// verify Python list-repr style tags (single quotes)
	require.Contains(t, out, "tags=['")
}

// TestGetFitsRelaxedSkeleton — relaxation NOTE header present.
func TestGetFitsRelaxedSkeleton(t *testing.T) {
	deps := liveDepsOrSkip(t)
	out, err := ExecuteTool(ctx(t), deps, "get_fits",
		map[string]any{"ship_name": "Gila", "context": "wormhole"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "NOTE: no community fits matched the full filter"),
		"expected NOTE: relaxation header, got: %s", out[:min(80, len(out))])
	require.Contains(t, out, "context='wormhole'")
	require.Contains(t, out, "FOUND")
}

// TestGetFitsNoRetriever — nil retriever returns the "not initialized" error string.
func TestGetFitsNoRetriever(t *testing.T) {
	deps := &Deps{} // Retriever is nil
	out, err := ExecuteTool(context.Background(), deps, "get_fits",
		map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Contains(t, out, "[get_fits error: the community-fit corpus is not configured")
}

// ─── list_fits tests ─────────────────────────────────────────────────────────

// TestListFitsNoRetriever — nil retriever returns the "not initialized" error string.
// Mirrors Python: "[list_fits error: the community-fit corpus is not configured on this server]"
func TestListFitsNoRetriever(t *testing.T) {
	deps := &Deps{} // Retriever is nil
	out, err := ExecuteTool(context.Background(), deps, "list_fits",
		map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Equal(t, "[list_fits error: the community-fit corpus is not configured on this server]", out)
}

// TestListFitsSkeleton — structural assertions against live Qdrant (corpus-gated).
func TestListFitsSkeleton(t *testing.T) {
	deps := liveDepsOrSkip(t)
	out, err := ExecuteTool(ctx(t), deps, "list_fits", map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	// Must produce either a hit listing or a NO FITS FOUND message — never empty.
	require.NotEmpty(t, out)
	if strings.HasPrefix(out, "NO FITS FOUND") {
		// Corpus may not have Gila fits indexed — acceptable in CI without corpus.
		t.Logf("list_fits returned NO FITS FOUND (corpus may be empty): %s", out)
		return
	}
	// Expect "{N} fit(s) match:\n{rows}"
	require.Contains(t, out, "fit(s) match")
	require.Contains(t, out, "| tags:")
	require.Contains(t, out, "1. ") // first row numbered
}

// TestListFitsNoFitsFormat — verifies exact NO FITS FOUND output format when filters match nothing.
// Uses a ship name that cannot exist so we always hit the zero-count path.
func TestListFitsNoFitsFormat(t *testing.T) {
	deps := liveDepsOrSkip(t)
	out, err := ExecuteTool(ctx(t), deps, "list_fits",
		map[string]any{"ship_name": "NoSuchShip99999XYZ"})
	require.NoError(t, err)
	// Exact Python format: "NO FITS FOUND for filters {'ship_name': 'NoSuchShip99999XYZ'}."
	require.Equal(t, "NO FITS FOUND for filters {'ship_name': 'NoSuchShip99999XYZ'}.", out)
}

// TestListFitsRoutes — verifies ExecuteTool dispatches "list_fits" without error
// (smoke test — does not require Qdrant).
func TestListFitsRoutes(t *testing.T) {
	// Use nil retriever; we just want to confirm the switch case is wired and
	// returns the graceful "not initialized" string (not "unknown tool" error).
	deps := &Deps{}
	out, err := ExecuteTool(context.Background(), deps, "list_fits", map[string]any{})
	require.NoError(t, err, "ExecuteTool must not return an error for list_fits (graceful nil-retriever path)")
	require.NotEqual(t, "", out)
	require.NotContains(t, out, "unknown tool")
	require.Contains(t, out, "list_fits error")
}

// TestFmtTagList — unit test for the _fmt_list port (no network needed).
func TestFmtTagList(t *testing.T) {
	require.Equal(t, "", fmtTagList(nil))
	require.Equal(t, "", fmtTagList([]string{}))
	require.Equal(t, "abyss", fmtTagList([]string{"abyss"}))
	require.Equal(t, "abyss,filament", fmtTagList([]string{"abyss", "filament"}))
	require.Equal(t, "abyss,filament,t3", fmtTagList([]string{"abyss", "filament", "t3"}))
}

const (
	gilaFitID1 = "6f1d3b2a-0c4e-5a7b-9d8e-1f2a3b4c5d6e"
	gilaFitID2 = "0b9c8d7e-6f5a-5b4c-8d3e-2f1a0b9c8d7e"
)

// twoGilaFitsScroll is a scroll answer with two workbench Gila fits that carry point ids.
const twoGilaFitsScroll = `{"result":{"points":[` +
	`{"id":"` + gilaFitID1 + `","payload":{"source":"workbench","ship_name":"Gila","fit_name":"Gila One","score":0.4,"fit_tags":["pve"],"source_url":"https://example.invalid/fit/1","text":"[Gila, Gila One]\nRapid Light Missile Launcher II"}},` +
	`{"id":"` + gilaFitID2 + `","payload":{"source":"workbench","ship_name":"Gila","fit_name":"Gila Two","score":0.3,"fit_tags":["pve"],"source_url":"https://example.invalid/fit/2","text":"[Gila, Gila Two]\nDamage Control II"}}]}}`

// fakeCorpusDeps serves every scroll with scrollBody and every count with the number of
// its points, behind a retriever; the deps are Public.
func fakeCorpusDeps(t *testing.T, scrollBody string) *Deps {
	t.Helper()
	var parsed struct {
		Result struct {
			Points []json.RawMessage `json:"points"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(scrollBody), &parsed))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/points/count") {
			fmt.Fprintf(w, `{"result":{"count":%d}}`, len(parsed.Result.Points))
			return
		}
		fmt.Fprint(w, scrollBody)
	}))
	t.Cleanup(srv.Close)
	return &Deps{Retriever: rag.NewQdrantRetriever(srv.URL, corpus.DefaultCollection, nil, 5), Public: true}
}

func TestGetFits_FitIDErrors(t *testing.T) {
	d := fakeCorpusDeps(t, `{"result":{"points":[]}}`)
	text, data, _ := getFits(context.Background(), d, map[string]any{"fit_id": "wiki-chunk-7"})
	require.Equal(t, "fit_id: not a fit id (a UUID from list_fits or get_fits).", text)
	require.Nil(t, data)
	text, data, _ = getFits(context.Background(), d, map[string]any{"fit_id": gilaFitID1})
	require.Equal(t, "No community fit with id "+gilaFitID1+".", text)
	require.Nil(t, data)
}

func TestGetFits_IncludeEFTFalseListsIDsOnly(t *testing.T) {
	d := fakeCorpusDeps(t, twoGilaFitsScroll)
	text, data, _ := getFits(context.Background(), d, map[string]any{"ship_name": "Gila", "include_eft": false})
	require.NotContains(t, text, "[Gila,")
	require.Contains(t, text, "| id=")
	require.True(t, strings.HasSuffix(text, "EFT blocks left out: fetch one fit with get_fits fit_id."))
	require.NotEmpty(t, data.Fits[0].FitID)
	require.Empty(t, data.Fits[0].EFT)
}

func TestGetFits_FitIDReturnsOneFitLikeAOneHitSearch(t *testing.T) {
	one := `{"result":{"points":[` + strings.SplitN(strings.TrimPrefix(twoGilaFitsScroll, `{"result":{"points":[`), "},{\"id", 2)[0] + `}]}}`
	d := fakeCorpusDeps(t, one)
	byID, data, err := getFits(context.Background(), d, map[string]any{"fit_id": strings.ToUpper(gilaFitID1), "ship_name": "Tengu"})
	require.NoError(t, err)
	viaSearch, _, err := getFits(context.Background(), d, map[string]any{"ship_name": "Gila"})
	require.NoError(t, err)
	require.Equal(t, viaSearch, byID, "fit_id ignores the other filters and answers like a one-hit search")
	require.Contains(t, byID, "FOUND 1 community fit(s):")
	require.Contains(t, byID, "| id="+gilaFitID1+" ---")
	require.Contains(t, byID, "[Gila, Gila One]")
	require.Equal(t, gilaFitID1, data.Fits[0].FitID)
}

func TestGetFits_ChatTextHasNoIDAndPublicHasOne(t *testing.T) {
	d := fakeCorpusDeps(t, twoGilaFitsScroll)
	d.Public = false
	text, data, _ := getFits(context.Background(), d, map[string]any{"ship_name": "Gila"})
	require.NotContains(t, text, "id=", "the chat text is unchanged")
	require.Equal(t, gilaFitID1, data.Fits[0].FitID, "the typed result always carries the id")
	d.Public = true
	text, _, _ = getFits(context.Background(), d, map[string]any{"ship_name": "Gila"})
	require.Contains(t, text, " | id="+gilaFitID1+" ---")
	require.Contains(t, text, "[Gila, Gila One]", "the EFT stays by default")
}

func TestListFits_PublicRowsEndWithTheID(t *testing.T) {
	d := fakeCorpusDeps(t, twoGilaFitsScroll)
	text, data, _ := listFits(context.Background(), d, map[string]any{})
	require.Contains(t, text, "| https://example.invalid/fit/1 | id="+gilaFitID1+"\n")
	require.True(t, strings.HasSuffix(text, "| id="+gilaFitID2))
	require.Equal(t, gilaFitID1, data.Fits[0].FitID)
	d.Public = false
	text, data, _ = listFits(context.Background(), d, map[string]any{})
	require.NotContains(t, text, "id=")
	require.Equal(t, gilaFitID1, data.Fits[0].FitID)
}
