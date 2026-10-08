package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for the QC2 §7 candidate-pool fix: a scroll pass pages through EVERY point
// its filter matches (up to fitSearchMaxPool) instead of ranking only the first
// limit×4 points in point-ID order.

// syntheticFits builds n Gila fit points in ID order. Point i carries the flat
// score 0.1 unless overridden; bestAt maps a 0-based index to a (name, score).
func syntheticFits(n int, bestAt map[int]struct {
	name  string
	score float64
}) []goldenPoint {
	pts := make([]goldenPoint, n)
	for i := range pts {
		name, score := fmt.Sprintf("fit-%04d", i), 0.1
		if b, ok := bestAt[i]; ok {
			name, score = b.name, b.score
		}
		pts[i] = goldenPoint{
			ID: fmt.Sprintf("p%05d", i+1), // lexicographic order == scroll order
			Payload: map[string]any{
				"ship_name": "Gila", "fit_name": name, "source": "workbench", "score": score,
				"fit_tags": []any{}, "text": "[Gila, x]\nHeavy Missile Launcher II",
			},
		}
	}
	return pts
}

type namedScore = struct {
	name  string
	score float64
}

func searchGila(t *testing.T, g *goldenQdrant) FitSearchResult {
	t.Helper()
	r := NewQdrantRetriever(g.srv.URL, "synthetic", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Limit: 5})
	require.NoError(t, err)
	return res
}

func TestSearchFits_scrollPagesThroughTheWholeFilteredSet(t *testing.T) {
	// 600 fits; the clear best sits at position 520 — far beyond the old limit×4
	// (= 20) pool AND beyond the first 256-point page.
	g := newScrollQdrant(t, syntheticFits(600, map[int]namedScore{520: {"best", 0.6}}))
	res := searchGila(t, g)

	require.Len(t, res.Hits, 5)
	require.Equal(t, "best", res.Hits[0].FitName)
	require.False(t, res.Relaxed)

	// Three pages, each ≤ 256, chained by next_page_offset.
	require.Len(t, g.requests, 3)
	for _, rq := range g.requests {
		require.LessOrEqual(t, rq.Limit, fitScrollPageSize)
	}
	require.Nil(t, g.requests[0].Offset)
	require.Equal(t, "p00257", g.requests[1].Offset)
	require.Equal(t, "p00513", g.requests[2].Offset)
}

func TestSearchFits_scrollPoolIsCappedAtFitSearchMaxPool(t *testing.T) {
	require.Equal(t, 1000, fitSearchMaxPool)
	// 1,300 fits: a good one inside the cap (index 900), a better one beyond it
	// (index 1100). The pass reads exactly fitSearchMaxPool points.
	g := newScrollQdrant(t, syntheticFits(1300, map[int]namedScore{900: {"inside-cap", 0.5}, 1100: {"beyond-cap", 0.6}}))
	res := searchGila(t, g)

	require.Equal(t, "inside-cap", res.Hits[0].FitName)
	require.NotContains(t, hitNames(res.Hits), "beyond-cap")

	total := 0
	for _, rq := range g.requests {
		require.LessOrEqual(t, rq.Limit, fitScrollPageSize)
		total += rq.Limit
	}
	require.Equal(t, fitSearchMaxPool, total, "pages must add up to the cap, not past it")
	require.Len(t, g.requests, 4, "256+256+256+232")
	require.Equal(t, 232, g.requests[3].Limit)
}

// The biggest hull in the prod mirror (Gila, 311 fits) fits the cap 3× over.
func TestSearchFits_biggestRealHullFitsTheCap(t *testing.T) {
	g := newScrollQdrant(t, syntheticFits(311, map[int]namedScore{310: {"last-by-id", 0.6}}))
	res := searchGila(t, g)
	require.Equal(t, "last-by-id", res.Hits[0].FitName, "the highest-ID fit of a 311-fit hull must still be ranked")
	require.Len(t, g.requests, 2)
}

func TestSearchFits_smallPoolIsOneRequest(t *testing.T) {
	g := newScrollQdrant(t, syntheticFits(40, nil))
	res := searchGila(t, g)
	require.Len(t, res.Hits, 5)
	require.Len(t, g.requests, 1)
	require.Equal(t, fitScrollPageSize, g.requests[0].Limit)
}

func TestScrollFitsAll_errorOnLaterPageIsReturned(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls >= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"result":{"points":[{"id":"a","payload":{"ship_name":"Gila"}}],"next_page_offset":"b"}}`))
	}))
	t.Cleanup(srv.Close)
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	_, err := r.scrollFitsAll(context.Background(), &QdrantFilter{}, fitSearchMaxPool)
	require.ErrorContains(t, err, "qdrant scroll HTTP 500")

	calls = 0 // the same server through SearchFits: page 2 fails the whole search
	_, err = r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila"})
	require.ErrorContains(t, err, "qdrant scroll HTTP 500")
}

func TestScrollFitsAll_stopsOnAnEmptyPageEvenWithAnOffset(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"result":{"points":[],"next_page_offset":"stuck"}}`))
	}))
	t.Cleanup(srv.Close)
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	hits, err := r.scrollFitsAll(context.Background(), &QdrantFilter{}, fitSearchMaxPool)
	require.NoError(t, err)
	require.Empty(t, hits)
	require.Equal(t, 1, calls, "a misbehaving server must not make the pager loop")
}

func TestScrollFitsAll_stampsHitsLikeScrollFitsRaw(t *testing.T) {
	g := newScrollQdrant(t, syntheticFits(3, nil))
	r := NewQdrantRetriever(g.srv.URL, "c", fakeEmbed{}, 5)
	all, err := r.scrollFitsAll(context.Background(), &QdrantFilter{}, 10)
	require.NoError(t, err)
	first, err := r.Scroll(context.Background(), &QdrantFilter{}, 10)
	require.NoError(t, err)
	require.Equal(t, first, all)
	require.InDelta(t, 0.35, float64(all[0].Score), 1e-6)
}

// The vector pass keeps its semantics: ONE search request for the top limit×4 by
// similarity, no paging (cosine order IS relevance order, so there is no ID-order
// truncation to defeat).
func TestSearchFits_vectorPathStillAsksForTopLimitTimesFour(t *testing.T) {
	var paths []string
	var limits []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(raw, &body)
		limits = append(limits, body.Limit)
		_, _ = w.Write([]byte(`{"result":[{"id":"a","score":0.9,"payload":{"ship_name":"Gila","fit_name":"v","source":"workbench","score":0.3}}]}`))
	}))
	t.Cleanup(srv.Close)
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Q: "missile gila", Limit: 5})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)
	require.Len(t, paths, 1)
	require.True(t, strings.HasSuffix(paths[0], "/points/search"), paths[0])
	require.Equal(t, []int{5 * fitSearchCandidateMultiplier}, limits)
}

// shipFits builds n workbench fits of one hull; names are "<hull>-<i>" and the first
// carries the given score so a hull's best fit is known.
func shipFits(hull string, n int, best float64, startID int) []goldenPoint {
	pts := make([]goldenPoint, n)
	for i := range pts {
		score := 0.1
		if i == 0 {
			score = best
		}
		pts[i] = goldenPoint{
			ID: fmt.Sprintf("p%05d", startID+i),
			Payload: map[string]any{
				"ship_name": hull, "fit_name": fmt.Sprintf("%s-%d", hull, i), "source": "workbench", "score": score,
				"fit_tags": []any{}, "text": "[" + hull + ", x]\nHeavy Missile Launcher II",
			},
		}
	}
	return pts
}

func TestSearchFits_shipNamesFilterAndPerShipGrouping(t *testing.T) {
	var pts []goldenPoint
	pts = append(pts, shipFits("Redeemer", 5, 0.5, 1)...)
	pts = append(pts, shipFits("Sin", 3, 0.4, 100)...)
	pts = append(pts, shipFits("Widow", 1, 0.3, 200)...)
	pts = append(pts, shipFits("Rifter", 9, 0.9, 300)...) // not in the class
	g := newScrollQdrant(t, pts)
	r := NewQdrantRetriever(g.srv.URL, "synthetic", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{
		ShipNames: []string{"Redeemer", "Sin", "Widow", "Panther"}, PerShip: 2, Limit: 24,
	})
	require.NoError(t, err)

	// Every fit of the class is counted; Rifter is filtered out.
	require.Equal(t, map[string]int{"Redeemer": 5, "Sin": 3, "Widow": 1}, res.ShipCounts)
	// At most PerShip hits per hull: 2 + 2 + 1, best-ranked first within a hull.
	per := map[string]int{}
	for _, h := range res.Hits {
		per[h.ShipName]++
		require.NotEqual(t, "Rifter", h.ShipName)
	}
	require.Equal(t, map[string]int{"Redeemer": 2, "Sin": 2, "Widow": 1}, per)
	require.Equal(t, "Redeemer-0", res.Hits[0].FitName)
}

func TestSearchFits_noPerShipLeavesCountsNil(t *testing.T) {
	g := newScrollQdrant(t, shipFits("Gila", 3, 0.5, 1))
	r := NewQdrantRetriever(g.srv.URL, "synthetic", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Limit: 5})
	require.NoError(t, err)
	require.Nil(t, res.ShipCounts)
	require.Len(t, res.Hits, 3)
}

func TestSearchFits_abyssPinnedWhenFacetsAreDropped(t *testing.T) {
	abyss := shipFits("Gila", 2, 0.5, 1)
	for i := range abyss {
		abyss[i].Payload["fit_tags"] = []any{"abyss", "abyss-t3"}
		abyss[i].Payload["filament_type"] = []any{"electrical"}
	}
	plain := shipFits("Rifter", 3, 0.9, 100) // a better, non-abyss fit
	g := newScrollQdrant(t, append(abyss, plain...))
	r := NewQdrantRetriever(g.srv.URL, "synthetic", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{Abyss: true, FilamentType: "dark", Tag: "abyss-t2", Limit: 5})
	require.NoError(t, err)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"tier=abyss-t2", "filament=dark"}, res.Dropped)
	require.NotEmpty(t, res.Hits)
	for _, h := range res.Hits {
		require.Equal(t, "Gila", h.ShipName, "a relaxed abyss search must stay on abyss fits")
	}
}

func TestSearchFits_acceptDropsCandidatesBeforeCountingAndRanking(t *testing.T) {
	var pts []goldenPoint
	pts = append(pts, shipFits("Sin", 4, 0.9, 1)...) // best scored, all rejected below
	pts = append(pts, shipFits("Panther", 3, 0.2, 100)...)
	g := newScrollQdrant(t, pts)
	r := NewQdrantRetriever(g.srv.URL, "synthetic", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{
		ShipNames: []string{"Sin", "Panther"}, PerShip: 2, Limit: 24,
		Accept: func(h FitSearchHit) bool { return h.ShipName != "Sin" },
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"Panther": 3}, res.ShipCounts)
	require.Len(t, res.Hits, 2)
	for _, h := range res.Hits {
		require.Equal(t, "Panther", h.ShipName)
	}
}
