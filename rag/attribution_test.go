package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/corpus"
	"github.com/stretchr/testify/require"
)

// --- the licence table ---------------------------------------------------------

func TestSourceLicenses_CoverEveryCorpusSource(t *testing.T) {
	want := append(corpus.FitSources(), corpus.SourceTypes()...)
	want = append(want, SourceWiki)
	for _, key := range want {
		l, ok := SourceLicenseOf(key)
		require.Truef(t, ok, "corpus source %q has no licence-table entry (core/rag/attribution.go)", key)
		require.NotEmptyf(t, l.License, "licence of %q is empty", key)
	}
	// No orphan rows either: every key of the table is a corpus source.
	require.ElementsMatch(t, want, LicenseSourceKeys())
}

func TestAttributionFor_FitSources(t *testing.T) {
	for _, src := range corpus.FitSources() {
		a := AttributionFor(src, "https://example.test/fit/1")
		require.Equal(t, src, a.Source)
		require.Equal(t, "https://example.test/fit/1", a.SourceURL)
		require.NotEmpty(t, a.License)
	}
	require.Equal(t, "gustavmannfred", AttributionFor(corpus.SourceGustavmannfred, "u").Author)
	require.Empty(t, AttributionFor(corpus.SourceWorkbench, "u").Author, "no author is invented for a source that names none")
}

func TestWikiAttribution_IsCCBYSAWithCredit(t *testing.T) {
	a := WikiAttribution("Drake", "https://wiki.eveuniversity.org/Drake")
	require.Equal(t, "Drake", a.Source)
	require.Equal(t, "https://wiki.eveuniversity.org/Drake", a.SourceURL)
	require.Equal(t, "CC BY-SA 4.0", a.License)
	require.Equal(t, "EVE University", a.Credit)
	require.Equal(t, "https://creativecommons.org/licenses/by-sa/4.0/", a.LicenseURL)

	ws := AttributionFor(corpus.SourceTypeWikiSupplement, "u")
	require.Equal(t, "CC BY-SA 4.0", ws.License, "wiki_supplement reworks wiki text: share-alike applies")
}

func TestAttributionFor_UnknownSourceIsRestrictive(t *testing.T) {
	require.Contains(t, AttributionFor("no-such-source", "u").License, "do not redistribute")
}

func TestAttributionJSONShape(t *testing.T) {
	raw, err := json.Marshal(AttributionFor(corpus.SourceWorkbench, "https://x"))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, "workbench", m["source"])
	require.Equal(t, "https://x", m["source_url"])
	require.Contains(t, m, "license")
	require.NotContains(t, m, "author", "author is omitted when unknown")
}

// --- hits carry it ---------------------------------------------------------------

func TestPayloadToFitHit_CarriesAttribution(t *testing.T) {
	h := payloadToFitHit(map[string]any{
		"source": "caldarijoans", "ship_name": "Gila", "source_url": "https://caldarijoans.streamlit.app/x",
	})
	require.Equal(t, "https://caldarijoans.streamlit.app/x", h.Attribution.SourceURL)
	require.Equal(t, "caldarijoans", h.Attribution.Author)

	// zkillboard_meta points have no source_url: the first killmail is the link.
	z := payloadToFitHit(map[string]any{
		"source": "zkillboard_meta", "ship_name": "Gila", "sample_killmail_ids": []any{float64(42)},
	})
	require.Equal(t, "https://zkillboard.com/kill/42/", z.Attribution.SourceURL)
}

func TestPayloadToFitSearchHit_CarriesAttribution(t *testing.T) {
	h := payloadToFitSearchHit(map[string]any{
		"source": "workbench", "ship_name": "Gila", "fit_name": "x", "url": "https://eveworkbench.com/fit/1",
	})
	require.Equal(t, "workbench", h.Attribution.Source)
	require.Equal(t, "https://eveworkbench.com/fit/1", h.Attribution.SourceURL, "the legacy url key still yields a link")
	require.NotEmpty(t, h.Attribution.License)
}

// --- the fit paths never serve non-fit documents ----------------------------------

// mixedCorpus is a corpus holding every kind of document: fits and the prose chunks
// (wiki page, wiki_supplement, ui_guide) that carry NO per-hit attribution on a fit
// path. The wiki chunk also carries a ship_name, the worst case.
func mixedCorpus() []goldenPoint {
	fit := func(id, src string) goldenPoint {
		return goldenPoint{ID: id, Payload: map[string]any{
			"source": src, "ship_name": "Gila", "fit_name": id, "doc_kind": "single_fit",
			"source_url": "https://fits.example.test/" + id, "score": 0.5, "fit_tags": []any{"pve"},
			"text": "[Gila, " + id + "]\nHeavy Missile Launcher II",
		}}
	}
	return []goldenPoint{
		fit("p01", corpus.SourceWorkbench),
		fit("p02", corpus.SourceAbyssTracker),
		fit("p03", corpus.SourceGustavmannfred),
		fit("p04", corpus.SourceCaldarijoans),
		{ID: "w01", Payload: map[string]any{
			"source": "Gila", "ship_name": "Gila", "source_url": "https://wiki.eveuniversity.org/Gila",
			"text": "The Gila is a Guristas cruiser.", "fit_tags": []any{"pve"},
		}},
		{ID: "w02", Payload: map[string]any{
			"source": "abyss_damage_profiles", "source_type": "wiki_supplement", "ship_name": "Gila",
			"url": "https://example.test/doc", "text": "Abyss damage profiles.",
		}},
		{ID: "w03", Payload: map[string]any{
			"source": "ui_guide_chat", "source_type": "ui_guide", "ship_name": "Gila",
			"url": "https://example.test/ui", "text": "How to use the chat.",
		}},
	}
}

// mixedQdrant serves scroll and count over pts with the same filter semantics as
// Qdrant for the shapes the fit paths build, and records every filter.
func mixedQdrant(t *testing.T, pts []goldenPoint) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			Filter goldenFilter `json:"filter"`
			Limit  int          `json:"limit"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		var admitted []goldenPoint
		for _, p := range pts {
			if req.Filter.admits(p.Payload) {
				admitted = append(admitted, p)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/points/count") {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": len(admitted)}}))
			return
		}
		if req.Limit > 0 && len(admitted) > req.Limit {
			admitted = admitted[:req.Limit]
		}
		if strings.HasSuffix(r.URL.Path, "/points/search") { // vector path: a bare list of scored hits
			hits := make([]map[string]any, len(admitted))
			for i, p := range admitted {
				hits[i] = map[string]any{"id": p.ID, "score": 0.9, "payload": p.Payload}
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": hits}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": admitted}}))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Contract: whatever filters a caller passes, the community-fit paths (SearchFits,
// ScrollFits, ScrollListFits: Product A's only corpus readers) return fit documents
// of a corpus.FitSources source and nothing else, each with a full Attribution. So
// wiki-derived text can never leave Product A without its CC BY-SA attribution,
// because it cannot leave at all.
func TestFitPaths_NeverServeNonFitDocuments(t *testing.T) {
	srv := mixedQdrant(t, mixedCorpus())
	r := NewQdrantRetriever(srv.URL, "mixed", fakeEmbed{}, 5)
	ctx := context.Background()
	fitSources := corpus.FitSources()

	// A caller can name any source it likes: the wiki page title, a source_type
	// document, or the empty default.
	sources := []string{"", "Gila", "abyss_damage_profiles", "ui_guide_chat", "wiki", "wiki_supplement", "ui_guide",
		corpus.SourceWorkbench, corpus.SourceZkillboardMeta}

	requireFitAttribution := func(label string, source string, a Attribution) {
		t.Helper()
		require.Containsf(t, fitSources, source, "%s: served a non-fit document (source %q)", label, source)
		require.Equalf(t, source, a.Source, "%s: attribution names the hit's source", label)
		require.NotEmptyf(t, a.SourceURL, "%s: attribution has no link", label)
		require.NotEmptyf(t, a.License, "%s: attribution has no licence", label)
	}

	for _, src := range sources {
		for _, q := range []string{"", "gila fit"} {
			res, err := r.SearchFits(ctx, FitSearchQuery{Source: src, ShipName: "Gila", Q: q, Limit: 50})
			require.NoError(t, err)
			for _, h := range res.Hits {
				requireFitAttribution(fmt.Sprintf("SearchFits(source=%q q=%q)", src, q), h.Source, h.Attribution)
			}
		}
		sc, err := r.ScrollFits(ctx, FitQuery{Source: src, ShipName: "Gila", Limit: 10})
		require.NoError(t, err)
		for _, h := range sc.Hits {
			requireFitAttribution(fmt.Sprintf("ScrollFits(source=%q)", src), h.Source, h.Attribution)
		}
		ls, err := r.ScrollListFits(ctx, ListFitQuery{Source: src, ShipName: "Gila", Limit: 200})
		require.NoError(t, err)
		for _, h := range ls.Points {
			requireFitAttribution(fmt.Sprintf("ScrollListFits(source=%q)", src), h.Source, h.Attribution)
		}
		if src != "" && !contains(fitSources, src) {
			require.Zerof(t, ls.Total, "list_fits counts nothing for the non-fit source %q", src)
			require.Empty(t, sc.Hits, "get_fits finds nothing for the non-fit source %q", src)
		}
	}
	// Empty filters over the whole corpus: still only fits.
	ls, err := r.ScrollListFits(ctx, ListFitQuery{})
	require.NoError(t, err)
	require.Equal(t, 4, ls.Total)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// --- no dump ---------------------------------------------------------------------

// No fit query has an offset, page or cursor: one query reaches one page. Adding one
// means deciding the page-depth cap first; this test is the reminder.
func TestFitQueries_HaveNoPaging(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[FitQuery](), reflect.TypeFor[ListFitQuery](), reflect.TypeFor[FitSearchQuery](),
	} {
		for f := range typ.Fields() {
			name := strings.ToLower(f.Name)
			for _, banned := range []string{"offset", "page", "cursor", "skip", "after"} {
				require.NotContainsf(t, name, banned, "%s.%s looks like paging: cap its depth (FitPageMax) before adding it", typ.Name(), f.Name)
			}
		}
	}
}

func manyFits(n int) []goldenPoint {
	pts := make([]goldenPoint, n)
	for i := range pts {
		pts[i] = goldenPoint{ID: fmt.Sprintf("f%04d", i), Payload: map[string]any{
			"source": "workbench", "ship_name": "Gila", "fit_name": fmt.Sprintf("fit-%d", i), "doc_kind": "single_fit",
			"source_url": fmt.Sprintf("https://eveworkbench.com/fit/%d", i), "score": 0.5, "fit_tags": []any{},
			"text": "[Gila, x]\nHeavy Missile Launcher II",
		}}
	}
	return pts
}

func TestSearchFits_PageIsCappedAndSaysSo(t *testing.T) {
	srv := mixedQdrant(t, manyFits(200))
	r := NewQdrantRetriever(srv.URL, "many", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{Limit: 100000}) // no filter at all
	require.NoError(t, err)
	require.Len(t, res.Hits, FitPageMax)
	require.Equal(t, FitPageMax, res.Limit)
	require.Contains(t, res.Note, "clamped")

	res, err = r.SearchFits(context.Background(), FitSearchQuery{Limit: 5})
	require.NoError(t, err)
	require.Len(t, res.Hits, 5)
	require.Equal(t, 5, res.Limit)
	require.Empty(t, res.Note, "no note when the request fit the cap")

	res, err = r.SearchFits(context.Background(), FitSearchQuery{})
	require.NoError(t, err)
	require.Len(t, res.Hits, FitPageMax, "an empty query lists the first page, not everything")
}

func TestScrollListFits_PageIsCapped(t *testing.T) {
	srv := mixedQdrant(t, manyFits(200))
	r := NewQdrantRetriever(srv.URL, "many", fakeEmbed{}, 5)

	res, err := r.ScrollListFits(context.Background(), ListFitQuery{Limit: 200})
	require.NoError(t, err)
	require.Equal(t, 200, res.Total, "the total stays exact")
	require.Len(t, res.Points, FitPageMax)
	require.True(t, res.Clamped)

	res, err = r.ScrollListFits(context.Background(), ListFitQuery{})
	require.NoError(t, err)
	require.Len(t, res.Points, FitPageMax, "the default page is the cap")
	require.False(t, res.Clamped)
}
