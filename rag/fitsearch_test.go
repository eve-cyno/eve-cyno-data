package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func facetLabels(facets []facetCondition) []string {
	out := make([]string, len(facets))
	for i, f := range facets {
		out[i] = f.label
	}
	return out
}

func TestBuildFitSearchConditions(t *testing.T) {
	q := FitSearchQuery{Activity: "pve", Tag: "abyss-t4", ShipName: "Gila", FilamentType: "Electrical", CostClass: "cheap"}
	essential, facets := buildFitSearchConditions(q)

	// The default fit-source MatchAny is always pinned as an essential.
	require.Len(t, essential, 2)
	require.Equal(t, "source", essential[0].Key)
	require.Equal(t, fitSearchSources, essential[0].Match["any"])
	require.Equal(t, "ship_name", essential[1].Key)
	require.Equal(t, "Gila", essential[1].Match["value"])

	// Facets are returned in drop order: cost → tier → filament → activity.
	require.Equal(t, []string{"cost=cheap", "tier=abyss-t4", "filament=Electrical", "activity=pve"}, facetLabels(facets))
	require.Equal(t, "fit_tags", facets[0].cond.Key)
	require.Equal(t, "cheap", facets[0].cond.Match["value"])
	require.Equal(t, "fit_tags", facets[1].cond.Key)
	require.Equal(t, "abyss-t4", facets[1].cond.Match["value"])
	require.Equal(t, "filament_type", facets[2].cond.Key)
	require.Equal(t, "Electrical", facets[2].cond.Match["value"])
	require.Equal(t, "fit_tags", facets[3].cond.Key)
	require.Equal(t, "pve", facets[3].cond.Match["value"])
}

func TestBuildFitSearchConditions_explicitSourceIsLastFacet(t *testing.T) {
	essential, facets := buildFitSearchConditions(FitSearchQuery{CostClass: "cheap", Source: "gustavmannfred"})

	// The default fit-source MatchAny stays pinned as an essential even with an
	// explicit source — dropping the explicit facet must relax back to the
	// default set, never to no source filter at all.
	require.Equal(t, "source", essential[0].Key)
	require.Equal(t, fitSearchSources, essential[0].Match["any"])

	// The explicit source is an ADDITIONAL narrowing facet that drops last, after cost.
	require.Equal(t, []string{"cost=cheap", "source=gustavmannfred"}, facetLabels(facets))
	require.Equal(t, "source", facets[1].cond.Key)
	require.Equal(t, "gustavmannfred", facets[1].cond.Match["value"])
}

func TestBuildFitSearchConditions_clone(t *testing.T) {
	alpha, _ := buildFitSearchConditions(FitSearchQuery{Clone: "alpha"})
	require.Equal(t, "is_alpha", alpha[len(alpha)-1].Key)
	require.Equal(t, true, alpha[len(alpha)-1].Match["value"])

	omega, _ := buildFitSearchConditions(FitSearchQuery{Clone: "omega"})
	require.Equal(t, "is_alpha", omega[len(omega)-1].Key)
	require.Equal(t, false, omega[len(omega)-1].Match["value"])

	none, _ := buildFitSearchConditions(FitSearchQuery{})
	for _, c := range none {
		require.NotEqual(t, "is_alpha", c.Key, "no clone filter → no is_alpha condition")
	}
}

func TestCompositeScore_ordersByQualityViewsTested(t *testing.T) {
	high := FitSearchHit{Score: 0.8, Views: 1000, Tested: true}
	low := FitSearchHit{Score: 0.5, Views: 10}
	require.Greater(t, compositeScore(high), compositeScore(low))
}

func TestCompositeScore_zeroViewsDoesNotCollapse(t *testing.T) {
	require.Greater(t, compositeScore(FitSearchHit{Score: 0.7, Views: 0}), 0.0)
}

func TestPayloadToFitSearchHit_extractsAndTolerates(t *testing.T) {
	pl := map[string]any{
		"ship_name": "Gila", "fit_name": "T4 EC", "source": "workbench",
		"source_url": "https://x", "text": "[Gila, T4]",
		"fit_tags": []any{"abyss", "t4"}, "views": float64(1234),
		"is_tested": true, "has_video": false, "is_alpha": true, "score": float64(0.8),
	}
	h := payloadToFitSearchHit(pl)
	require.Equal(t, "Gila", h.ShipName)
	require.Equal(t, "T4 EC", h.FitName)
	require.Equal(t, []string{"abyss", "t4"}, h.Tags)
	require.Equal(t, 1234, h.Views)
	require.True(t, h.Tested)
	require.True(t, h.Alpha)
	require.InDelta(t, 0.8, h.Score, 1e-9)

	h2 := payloadToFitSearchHit(map[string]any{"ship_name": "Vagabond"})
	require.Equal(t, "Vagabond", h2.ShipName)
	require.Zero(t, h2.Views)
	require.False(t, h2.Tested)
}

type fakeEmbed struct{ err error }

func (f fakeEmbed) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

const scrollTwoFits = `{"result":{"points":[
 {"id":"a","payload":{"ship_name":"Gila","fit_name":"T4 EC","source":"workbench","source_url":"u1","fit_tags":["abyss","t4"],"views":1000,"is_tested":true,"score":0.8,"text":"[Gila, T4]"}},
 {"id":"b","payload":{"ship_name":"Gila","fit_name":"budget","source":"workbench","source_url":"u2","fit_tags":["abyss"],"views":10,"is_tested":false,"score":0.5,"text":"[Gila, b]"}}
]}}`

func TestSearchFits_scrollPath_ranksByComposite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/points/scroll")
		io.WriteString(w, scrollTwoFits)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{Activity: "pve", Tag: "abyss"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Total)
	require.False(t, res.Relaxed)
	require.Equal(t, "T4 EC", res.Hits[0].FitName)
	require.Equal(t, "budget", res.Hits[1].FitName)
	// scroll path ranks by compositeScore, not the raw payload score field
	require.Equal(t, compositeScore(res.Hits[0]), res.Hits[0].Relevance)
	require.Greater(t, res.Hits[0].Relevance, res.Hits[1].Relevance)
}

func TestSearchFits_skipsNonFitEntries(t *testing.T) {
	// One real fit + two non-fit corpus entries (no ship_name, e.g. caldarijoans'
	// enemy DB / FAQ pages) — only the real fit survives.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"result":{"points":[
 {"id":"1","payload":{"ship_name":"Gila","fit_name":"real","source":"workbench"}},
 {"id":"2","payload":{"ship_name":"  ","fit_name":"enemy-db","source":"caldarijoans"}},
 {"id":"3","payload":{"fit_name":"faq","source":"caldarijoans"}}
]}}`)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Total)
	require.Equal(t, "Gila", res.Hits[0].ShipName)
}

func TestSearchFits_vectorPath_usesSearchEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/points/search")
		io.WriteString(w, `{"result":[
 {"id":"a","score":0.42,"payload":{"ship_name":"Gila","fit_name":"low-sim","source":"workbench","views":9000}},
 {"id":"b","score":0.91,"payload":{"ship_name":"Gila","fit_name":"high-sim","source":"workbench","views":1}}
]}`)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{Q: "cheap gila abyss"})
	require.NoError(t, err)
	require.Equal(t, "high-sim", res.Hits[0].FitName)
}

func TestSearchFits_relaxesWhenFacetsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "filament_type") {
			io.WriteString(w, `{"result":{"points":[]}}`)
			return
		}
		io.WriteString(w, scrollTwoFits)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", FilamentType: "Gamma"})
	require.NoError(t, err)
	require.True(t, res.Relaxed)
	require.Contains(t, res.Dropped, "filament=Gamma")
	require.Equal(t, 2, res.Total)
}

func TestSearchFits_embedFailureDegradesToScroll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/points/scroll")
		io.WriteString(w, scrollTwoFits)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{err: io.ErrUnexpectedEOF}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{Q: "gila"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Total)
}

// oneGila is a single-point scroll response used to prove a hit survives
// stepwise relaxation once the offending facet is dropped.
const oneGila = `{"result":{"points":[
 {"id":"g","payload":{"ship_name":"Gila","fit_name":"solo pvp","source":"workbench","source_url":"u","fit_tags":["pvp"],"views":100,"is_tested":true,"score":0.7,"text":"[Gila]"}}
]}}`

// requireNoMustValueCondition asserts the parsed filter's `must` clause carries
// no condition on key with match.value == value (used to prove the explicit
// narrowing facet was dropped while a MatchAny pin on the same key may remain).
func requireNoMustValueCondition(t *testing.T, filter map[string]any, key, value string) {
	t.Helper()
	must, _ := filter["must"].([]any)
	for _, raw := range must {
		cond, ok := raw.(map[string]any)
		if !ok || cond["key"] != key {
			continue
		}
		match, _ := cond["match"].(map[string]any)
		if match["value"] == value {
			t.Fatalf("must clause still carries %s=%s after relaxation", key, value)
		}
	}
}

// requireMustAnyCondition asserts the parsed filter's `must` clause carries a
// MatchAny condition on key covering exactly the given values (used to prove
// the default fit-source pin survives every relaxation pass).
func requireMustAnyCondition(t *testing.T, filter map[string]any, key string, values []string) {
	t.Helper()
	must, _ := filter["must"].([]any)
	for _, raw := range must {
		cond, ok := raw.(map[string]any)
		if !ok || cond["key"] != key {
			continue
		}
		match, _ := cond["match"].(map[string]any)
		anyRaw, ok := match["any"].([]any)
		if !ok {
			continue
		}
		got := make([]string, 0, len(anyRaw))
		for _, v := range anyRaw {
			s, _ := v.(string)
			got = append(got, s)
		}
		require.Equal(t, values, got)
		return
	}
	t.Fatalf("must clause missing a %q MatchAny condition", key)
}

// requireMustCondition asserts the parsed filter's `must` clause carries a
// condition matching key=value (used to prove essentials survive relaxation).
func requireMustCondition(t *testing.T, filter map[string]any, key, value string) {
	t.Helper()
	must, _ := filter["must"].([]any)
	for _, raw := range must {
		cond, ok := raw.(map[string]any)
		if !ok || cond["key"] != key {
			continue
		}
		match, _ := cond["match"].(map[string]any)
		if match["value"] == value {
			return
		}
	}
	t.Fatalf("must clause missing %q=%q condition", key, value)
}

// explicitGustavCond is the JSON rendering of the explicit-source narrowing
// condition's match. The bare string "gustavmannfred" appears in EVERY request
// (it is part of the always-pinned MatchAny default set), so fakes and
// assertions must target the value-match form specifically.
const explicitGustavCond = `"value":"gustavmannfred"`

// TestSearchFits_relaxDropsExplicitSourceAndReportsIt covers brief scenarios
// (a)-(c): an impossible explicit source is dropped one facet at a time, the hit
// surfaces, Dropped names exactly the source facet, and the final request keeps
// ship_name + the quarantine must_not while shedding the explicit source condition.
func TestSearchFits_relaxDropsExplicitSourceAndReportsIt(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = b
		if strings.Contains(string(b), explicitGustavCond) {
			io.WriteString(w, `{"result":{"points":[]}}`)
			return
		}
		io.WriteString(w, oneGila)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Source: "gustavmannfred"})
	require.NoError(t, err)

	// (a) the hit is carried through after relaxation.
	require.Equal(t, 1, res.Total)
	require.Equal(t, "Gila", res.Hits[0].ShipName)
	require.True(t, res.Relaxed)
	// (b) exactly the source facet is reported dropped.
	require.Equal(t, []string{"source=gustavmannfred"}, res.Dropped)

	// (c) the LAST request no longer narrows to the explicit source, but keeps
	// ship_name and the quarantine must_not.
	require.NotContains(t, string(lastBody), explicitGustavCond)
	var req map[string]any
	require.NoError(t, json.Unmarshal(lastBody, &req))
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireNoMustValueCondition(t, filter, "source", "gustavmannfred")
	requireMustCondition(t, filter, "ship_name", "Gila")
	requireQuarantineMustNot(t, filter)
}

// TestSearchFits_droppedSourceFallsBackToDefaultSet is the review-fix regression
// for commit be2bd51: dropping the explicit source facet must relax back to the
// default fit-source MatchAny set, NOT to no source filter at all — otherwise a
// fully-relaxed search can surface non-fit corpus points (zkillboard_meta, wiki
// docs with a ship_name payload).
func TestSearchFits_droppedSourceFallsBackToDefaultSet(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = b
		if strings.Contains(string(b), explicitGustavCond) {
			io.WriteString(w, `{"result":{"points":[]}}`)
			return
		}
		io.WriteString(w, oneGila)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Source: "gustavmannfred"})
	require.NoError(t, err)
	require.True(t, res.Relaxed)
	require.Equal(t, 1, res.Total)

	// The LAST (relaxed) request still pins the full default MatchAny source set
	// alongside ship_name and the quarantine must_not.
	var req map[string]any
	require.NoError(t, json.Unmarshal(lastBody, &req))
	filter, ok := req["filter"].(map[string]any)
	require.True(t, ok)
	requireMustAnyCondition(t, filter, "source", fitSearchSources)
	requireMustCondition(t, filter, "ship_name", "Gila")
	requireQuarantineMustNot(t, filter)
}

// TestSearchFits_relaxDropsCostBeforeSource covers brief scenario (d): with both
// an impossible cost AND an impossible source, cost drops first, then source, and
// Dropped is ordered ["cost=cheap","source=gustavmannfred"].
func TestSearchFits_relaxDropsCostBeforeSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(body, "cheap") || strings.Contains(body, explicitGustavCond) {
			io.WriteString(w, `{"result":{"points":[]}}`)
			return
		}
		io.WriteString(w, oneGila)
	}))
	defer srv.Close()

	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)
	res, err := r.SearchFits(context.Background(), FitSearchQuery{
		ShipName: "Gila", CostClass: "cheap", Source: "gustavmannfred",
	})
	require.NoError(t, err)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"cost=cheap", "source=gustavmannfred"}, res.Dropped)
	require.Equal(t, 1, res.Total)
}

// scrollTestedVsUntested returns a strong untested fit (higher base score) ahead
// of a weaker tested fit, so the tested-boost is load-bearing: only the 2× boost
// can lift the tested fit above the untested one.
const scrollTestedVsUntested = `{"result":{"points":[
 {"id":"u","payload":{"ship_name":"Gila","fit_name":"untested-strong","source":"workbench","source_url":"u1","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.8,"text":"[Gila,u]"}},
 {"id":"t","payload":{"ship_name":"Gila","fit_name":"tested-weak","source":"workbench","source_url":"u2","fit_tags":["pvp"],"views":0,"is_tested":true,"score":0.5,"text":"[Gila,t]"}}
]}}`

// TestSearchFits_testedBoostFloatsTestedFirst covers point 5 (D5): Tested=true in
// the query doubles the composite score of tested hits in rankAndTrim. The control
// (no Tested) keeps the stronger-base untested fit first, proving the boost flips it.
func TestSearchFits_testedBoostFloatsTestedFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollTestedVsUntested)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	// Control: without Tested, the higher-base untested fit ranks first.
	plain, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila"})
	require.NoError(t, err)
	require.Equal(t, "untested-strong", plain.Hits[0].FitName)

	// Boost: Tested=true doubles the tested fit's composite → it ranks first.
	boosted, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Tested: true})
	require.NoError(t, err)
	require.Equal(t, "tested-weak", boosted.Hits[0].FitName)
}

// scrollEqualScoreTackleVsNoTackle gives both hits an identical base composite
// (Score 0.5, no views, untested) — the only thing distinguishing them is that
// "tackle" carries a Warp Scrambler II in its EFT text. Order returned is
// [no-tackle, tackle] so a stable sort with no boost applied preserves that order.
const scrollEqualScoreTackleVsNoTackle = `{"result":{"points":[
 {"id":"nt","payload":{"ship_name":"Stabber","fit_name":"no-tackle","source":"workbench","source_url":"u1","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.5,"text":"[Stabber, nt]\nDamage Control II"}},
 {"id":"t","payload":{"ship_name":"Stabber","fit_name":"tackle","source":"workbench","source_url":"u2","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.5,"text":"[Stabber, t]\nWarp Scrambler II"}}
]}}`

// TestSearchFits_pvpTackleBoostFloatsTackleFirst covers scenario 1: with equal
// base composite scores, Activity="pvp" quadruples the composite of the hit whose
// EFT carries a tackle module, so it ranks first ahead of an otherwise-identical
// tackle-less hit.
func TestSearchFits_pvpTackleBoostFloatsTackleFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollEqualScoreTackleVsNoTackle)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber", Activity: "pvp"})
	require.NoError(t, err)
	require.Equal(t, "tackle", res.Hits[0].FitName)
}

// TestSearchFits_noPvpTackleBoostOutsidePvpActivity covers scenario 2: with the
// same equal-score hits, Activity="" and Activity="pve" apply NO tackle boost —
// the pass-through order from the (stable-sorted, equal-composite) input is kept,
// i.e. the tackle hit does NOT get artificially promoted.
func TestSearchFits_noPvpTackleBoostOutsidePvpActivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollEqualScoreTackleVsNoTackle)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	for _, activity := range []string{"", "pve"} {
		res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber", Activity: activity})
		require.NoError(t, err)
		require.Equal(t, "no-tackle", res.Hits[0].FitName, "activity=%q must not apply the pvp tackle boost", activity)
		require.Equal(t, res.Hits[0].Relevance, compositeScore(res.Hits[0]), "activity=%q: relevance must equal the unboosted composite", activity)
	}
}

// scrollTackleLowVsHighNoTackle gives the tackle hit a much lower base score
// (0.2) than the tackle-less hit (0.55) — realistic gap sizes lifted straight
// from the task: only the ×4 pvp tackle boost (0.2*4=0.8 > 0.55) can flip the
// ranking.
const scrollTackleLowVsHighNoTackle = `{"result":{"points":[
 {"id":"nt","payload":{"ship_name":"Stabber","fit_name":"notackle-strong","source":"workbench","source_url":"u1","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.55,"text":"[Stabber, nt]\nDamage Control II"}},
 {"id":"t","payload":{"ship_name":"Stabber","fit_name":"tackle-weak","source":"workbench","source_url":"u2","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.2,"text":"[Stabber, t]\nWarp Scrambler II"}}
]}}`

// TestSearchFits_pvpTackleBoostOutranksHigherBaseScore covers scenario 3: the
// ×4 pvp tackle boost dominates a realistic base-score gap, flipping a
// lower-scored tackle-fitted hit above a higher-scored tackle-less hit. The
// control (no Activity) proves the base ranking would otherwise favor the
// tackle-less hit.
func TestSearchFits_pvpTackleBoostOutranksHigherBaseScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollTackleLowVsHighNoTackle)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	// Control: without the pvp boost, the higher-base tackle-less fit ranks first.
	plain, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber"})
	require.NoError(t, err)
	require.Equal(t, "notackle-strong", plain.Hits[0].FitName)

	// Boost: Activity="pvp" quadruples the tackle hit's composite → it ranks first.
	boosted, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber", Activity: "pvp"})
	require.NoError(t, err)
	require.Equal(t, "tackle-weak", boosted.Hits[0].FitName)
}

// pvpTagCond is the JSON rendering of the activity=pvp facet's match — present
// only on passes that still carry the facet, so fakes can distinguish the strict
// pass from relaxed ones.
const pvpTagCond = `"value":"pvp"`

// tristanPvpTaggedNoTackle is the single pvp-tagged, tackle-less hit returned by
// the strict activity=pvp pass in the Tristan scenario (Q156): non-empty, so
// plain relaxation would stop here even though tackle-fitted (untagged) Tristan
// fits exist one facet-drop away.
const tristanPvpTaggedNoTackle = `{"result":{"points":[
 {"id":"wh","payload":{"ship_name":"Tristan","fit_name":"Wormhole Tristan","source":"workbench","source_url":"u1","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.9,"text":"[Tristan, wh]\nDamage Control II"}}
]}}`

// tristanRelaxedWithTackle is the relaxed (activity facet dropped) result set:
// a stronger tackle-less training fit plus a weaker tackle-fitted one, so only
// the ×4 pvp tackle boost puts the tackle fit on top.
const tristanRelaxedWithTackle = `{"result":{"points":[
 {"id":"tr","payload":{"ship_name":"Tristan","fit_name":"training","source":"workbench","source_url":"u2","fit_tags":["training"],"views":0,"is_tested":false,"score":0.55,"text":"[Tristan, tr]\nDamage Control II"}},
 {"id":"sc","payload":{"ship_name":"Tristan","fit_name":"scram-kite","source":"workbench","source_url":"u3","fit_tags":[],"views":0,"is_tested":false,"score":0.2,"text":"[Tristan, sc]\nWarp Scrambler II"}}
]}}`

// TestSearchFits_pvpInsufficientSetKeepsRelaxingTowardTackle covers the Tristan
// scenario (Q156): the strict activity=pvp pass returns a NON-EMPTY set whose
// hits all lack tackle. Under Activity="pvp" that counts as insufficient — the
// loop keeps relaxing, the dropped facet is reported, and the tackle-fitted hit
// from the relaxed pass wins via the ×4 boost.
func TestSearchFits_pvpInsufficientSetKeepsRelaxingTowardTackle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), pvpTagCond) {
			io.WriteString(w, tristanPvpTaggedNoTackle)
			return
		}
		io.WriteString(w, tristanRelaxedWithTackle)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Tristan", Activity: "pvp"})
	require.NoError(t, err)
	require.Equal(t, "scram-kite", res.Hits[0].FitName)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"activity=pvp"}, res.Dropped)
}

// TestSearchFits_pvpNoTackleAnywhereFallsBackToFirstSet covers the fallback leg:
// when NO pass ever yields a tackle-fitted hit, SearchFits returns the first
// non-empty (pvp-tagged) set unchanged — never worse results than plain
// relaxation, and no phantom Dropped labels.
func TestSearchFits_pvpNoTackleAnywhereFallsBackToFirstSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), pvpTagCond) {
			io.WriteString(w, tristanPvpTaggedNoTackle)
			return
		}
		// Relaxed pass: still no tackle anywhere.
		io.WriteString(w, `{"result":{"points":[
 {"id":"tr","payload":{"ship_name":"Tristan","fit_name":"training","source":"workbench","source_url":"u2","fit_tags":["training"],"views":0,"is_tested":false,"score":0.55,"text":"[Tristan, tr]\nDamage Control II"}}
]}}`)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Tristan", Activity: "pvp"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Total)
	require.Equal(t, "Wormhole Tristan", res.Hits[0].FitName)
	require.False(t, res.Relaxed)
	require.Empty(t, res.Dropped)
}

// scrollRailgunVsBlaster gives the blaster hit a higher base score (0.55) than
// the railgun hit (0.2) — the same realistic gap lifted from the pvp tackle
// scenario (task Q141: "Build me a Cormorant fit for nullsec railgun
// roaming" was serving a blaster fit because retrieval had no weapon-family
// slot). Only the ×4 weapon-family boost (0.2*4=0.8 > 0.55) can flip the
// ranking in the boosted case.
const scrollRailgunVsBlaster = `{"result":{"points":[
 {"id":"b","payload":{"ship_name":"Cormorant","fit_name":"blaster-strong","source":"workbench","source_url":"u1","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.55,"text":"[Cormorant, b]\nNeutron Blaster Cannon II"}},
 {"id":"r","payload":{"ship_name":"Cormorant","fit_name":"railgun-weak","source":"workbench","source_url":"u2","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.2,"text":"[Cormorant, r]\n150mm Railgun II"}}
]}}`

// TestSearchFits_weaponFamilyBoostOutranksHigherBaseScore covers ranking (a):
// with WeaponFamily="railgun", the ×4 boost promotes the lower-scored railgun
// hit above the higher-scored blaster hit. The control (no WeaponFamily) proves
// the base ranking would otherwise favor the blaster fit.
func TestSearchFits_weaponFamilyBoostOutranksHigherBaseScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollRailgunVsBlaster)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	// Control: without the weapon-family boost, the higher-base blaster fit ranks first.
	plain, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Cormorant"})
	require.NoError(t, err)
	require.Equal(t, "blaster-strong", plain.Hits[0].FitName)

	// Boost: WeaponFamily="railgun" quadruples the railgun hit's composite → it ranks first.
	boosted, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Cormorant", WeaponFamily: "railgun"})
	require.NoError(t, err)
	require.Equal(t, "railgun-weak", boosted.Hits[0].FitName)
}

// weaponFamilyCostCond distinguishes the strict cost=cheap pass (still
// carrying the facet) from the relaxed one, mirroring pvpTagCond. CostClass
// (not Activity) is the droppable facet here so this test exercises the
// weapon-family sufficiency rule in isolation from the pre-existing pvp
// tackle sufficiency rule (already covered by TestSearchFits_pvpInsufficient*)
// — the AND-composition of the two is a ranking/boost-layering concern, not
// something this scenario needs to also exercise.
const weaponFamilyCostCond = `"value":"cheap"`

// cormorantCostTaggedBlasterOnly is the strict cost=cheap pass result:
// non-empty (pvp-tagged, per the community corpus), but every hit is a
// blaster fit — no railgun marker anywhere.
const cormorantCostTaggedBlasterOnly = `{"result":{"points":[
 {"id":"b","payload":{"ship_name":"Cormorant","fit_name":"Nullsec Blaster Cormorant","source":"workbench","source_url":"u1","fit_tags":["pvp","cheap"],"views":0,"is_tested":false,"score":0.9,"text":"[Cormorant, b]\nNeutron Blaster Cannon II"}}
]}}`

// cormorantRelaxedWithRailgun is the relaxed (cost facet dropped) result set:
// a stronger blaster fit plus a weaker railgun-fitted one, so only the ×4
// weapon-family boost puts the railgun fit on top.
const cormorantRelaxedWithRailgun = `{"result":{"points":[
 {"id":"b2","payload":{"ship_name":"Cormorant","fit_name":"blaster-training","source":"workbench","source_url":"u2","fit_tags":["pvp","training"],"views":0,"is_tested":false,"score":0.55,"text":"[Cormorant, b2]\nNeutron Blaster Cannon II"}},
 {"id":"r","payload":{"ship_name":"Cormorant","fit_name":"railgun-roam","source":"workbench","source_url":"u3","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.2,"text":"[Cormorant, r]\n150mm Railgun II"}}
]}}`

// TestSearchFits_weaponFamilyInsufficientSetKeepsRelaxing covers sufficiency
// (b) — the Q141 scenario: the strict pass returns a NON-EMPTY, pvp-tagged set
// whose hits all lack the requested weapon family. Under WeaponFamily="railgun"
// that counts as insufficient — the loop keeps relaxing, the dropped facet is
// reported, and the railgun-fitted hit from the relaxed pass wins via the ×4
// boost.
func TestSearchFits_weaponFamilyInsufficientSetKeepsRelaxing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), weaponFamilyCostCond) {
			io.WriteString(w, cormorantCostTaggedBlasterOnly)
			return
		}
		io.WriteString(w, cormorantRelaxedWithRailgun)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Cormorant", CostClass: "cheap", WeaponFamily: "railgun"})
	require.NoError(t, err)
	require.Equal(t, "railgun-roam", res.Hits[0].FitName)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"cost=cheap"}, res.Dropped)
}

// TestSearchFits_noWeaponFamilyQueryUnaffected covers (c): a query with no
// WeaponFamily set gets zero behavior change — the pre-existing tackle-boost
// scenario continues to pick the tackle-fitted hit exactly as before, proving
// the weapon-family plumbing is fully inert when unset.
func TestSearchFits_noWeaponFamilyQueryUnaffected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollTackleLowVsHighNoTackle)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	boosted, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber", Activity: "pvp"})
	require.NoError(t, err)
	require.Equal(t, "tackle-weak", boosted.Hits[0].FitName)
}

// TestSearchFits_nonPvpTackleLessSetNoExtraPasses pins the guard rail: outside
// Activity="pvp", a non-empty tackle-less first-pass set is returned immediately
// — exactly ONE Qdrant request, no tackle-driven relaxation.
func TestSearchFits_nonPvpTackleLessSetNoExtraPasses(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, scrollTwoFits) // no tackle markers in either EFT
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", Activity: "pve"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Total)
	require.False(t, res.Relaxed)
	require.Equal(t, 1, calls, "non-pvp query must stop at the first non-empty pass")
}

// TestBoostedCompositeTankType verifies the ×4 tank-type ranking boost: with
// equal base Score, a hit whose EFT carries an armor-tank marker (see
// tankTypeMarkers) outranks an otherwise-identical shield-fitted hit once
// boostSpec.tankType="armor" is set.
func TestBoostedCompositeTankType(t *testing.T) {
	armorHit := FitSearchHit{Score: 1, EFT: "[Vexor, a]\n1600mm Steel Plates II"}
	shieldHit := FitSearchHit{Score: 1, EFT: "[Vexor, s]\nLarge Shield Extender II"}
	spec := boostSpec{tankType: "armor"}
	require.Greater(t, boostedComposite(armorHit, spec), boostedComposite(shieldHit, spec))
}

// tankTypeCostCond distinguishes the strict cost=cheap pass (still carrying
// the facet) from the relaxed one, mirroring weaponFamilyCostCond — exercises
// tank-type sufficiency in isolation via a droppable facet unrelated to
// Activity/WeaponFamily.
const tankTypeCostCond = `"value":"cheap"`

// vexorCostTaggedShieldOnly is the strict cost=cheap pass result: non-empty
// (pvp-tagged, cheap), but every hit is shield-fitted — no armor marker
// anywhere.
const vexorCostTaggedShieldOnly = `{"result":{"points":[
 {"id":"s","payload":{"ship_name":"Vexor","fit_name":"Cheap Shield Vexor","source":"workbench","source_url":"u1","fit_tags":["pvp","cheap"],"views":0,"is_tested":false,"score":0.9,"text":"[Vexor, s]\nLarge Shield Extender II"}}
]}}`

// vexorRelaxedWithArmor is the relaxed (cost facet dropped) result set: a
// stronger shield fit plus a weaker armor-fitted one, so only the ×4
// tank-type boost puts the armor fit on top.
const vexorRelaxedWithArmor = `{"result":{"points":[
 {"id":"s2","payload":{"ship_name":"Vexor","fit_name":"shield-training","source":"workbench","source_url":"u2","fit_tags":["pvp","training"],"views":0,"is_tested":false,"score":0.55,"text":"[Vexor, s2]\nLarge Shield Extender II"}},
 {"id":"a","payload":{"ship_name":"Vexor","fit_name":"armor-brawl","source":"workbench","source_url":"u3","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.2,"text":"[Vexor, a]\n1600mm Steel Plates II"}}
]}}`

// TestSearchFitsTankTypeSufficiency covers the tank-type sufficiency gate,
// mirroring TestSearchFits_weaponFamilyInsufficientSetKeepsRelaxing: the
// strict pass returns a NON-EMPTY, cost-tagged set whose hits all lack the
// requested tank type. Under TankType="armor" that counts as insufficient —
// the loop keeps relaxing, the dropped facet is reported, and the
// armor-fitted hit from the relaxed pass wins via the ×4 boost.
func TestSearchFitsTankTypeSufficiency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), tankTypeCostCond) {
			io.WriteString(w, vexorCostTaggedShieldOnly)
			return
		}
		io.WriteString(w, vexorRelaxedWithArmor)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Vexor", CostClass: "cheap", TankType: "armor"})
	require.NoError(t, err)
	require.Equal(t, "armor-brawl", res.Hits[0].FitName)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"cost=cheap"}, res.Dropped)
}

// TestBoostedCompositeFilamentHardeners verifies the ×2 filament-hardener
// ranking boost: with equal base Score, a hit whose EFT carries a hardener
// matching the filament's expected damage type (see filamentExpectedHardeners)
// outranks an otherwise-identical hit with no hardener at all, once
// boostSpec.filament="electrical" is set. Marker table content comes from
// data/knowledge/abyss_damage_profiles.md.
func TestBoostedCompositeFilamentHardeners(t *testing.T) {
	matched := FitSearchHit{Score: 1, EFT: "[Gila, e]\n" + filamentExpectedHardeners["electrical"][0] + " II"}
	unmatched := FitSearchHit{Score: 1, EFT: "[Gila, x]\nDamage Control II"}
	spec := boostSpec{filament: "electrical"}
	require.Greater(t, boostedComposite(matched, spec), boostedComposite(unmatched, spec))
}

// TestBoostedCompositeFilamentHardeners_noBoostWhenUnset covers the inert
// case: an empty spec.filament (the default when the query names no filament)
// applies NO boost — the hardener-carrying hit gets no artificial promotion,
// mirroring TestSearchFits_noWeaponFamilyQueryUnaffected's guard rail.
func TestBoostedCompositeFilamentHardeners_noBoostWhenUnset(t *testing.T) {
	hit := FitSearchHit{Score: 1, EFT: "[Gila, e]\n" + filamentExpectedHardeners["electrical"][0] + " II"}
	require.Equal(t, compositeScore(hit), boostedComposite(hit, boostSpec{}))
}

// scrollElectricalHardenerVsPlain gives the EM-hardened hit a lower base score
// (0.5) than the plain hit (0.9) — since the filament boost is ×2 (weaker than
// the ×4 tackle/weapon-family/tank-type boosts), only a gap under 2× can be
// flipped: 0.5*2=1.0 > 0.9.
const scrollElectricalHardenerVsPlain = `{"result":{"points":[
 {"id":"p","payload":{"ship_name":"Gila","fit_name":"plain-strong","source":"workbench","source_url":"u1","fit_tags":["abyss"],"filament_type":["electrical"],"views":0,"is_tested":false,"score":0.9,"text":"[Gila, p]\nDamage Control II"}},
 {"id":"h","payload":{"ship_name":"Gila","fit_name":"em-hardened-weak","source":"workbench","source_url":"u2","fit_tags":["abyss"],"filament_type":["electrical"],"views":0,"is_tested":false,"score":0.5,"text":"[Gila, h]\nEM Shield Hardener II"}}
]}}`

// TestSearchFits_filamentBoostOutranksHigherBaseScore proves the end-to-end
// wiring: FitSearchQuery.FilamentType flows into boostSpec.filament inside
// SearchFits (not just the unit-level boostedComposite call above), and the
// ×2 boost is strong enough to flip a realistic base-score gap in favor of the
// hardener-carrying hit. The control (no FilamentType) proves the base
// ranking would otherwise favor the plain hit.
func TestSearchFits_filamentBoostOutranksHigherBaseScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, scrollElectricalHardenerVsPlain)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	// Control: without FilamentType, the higher-base plain fit ranks first.
	plain, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila"})
	require.NoError(t, err)
	require.Equal(t, "plain-strong", plain.Hits[0].FitName)

	// Boost: FilamentType="electrical" doubles the EM-hardened hit's composite → it ranks first.
	boosted, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Gila", FilamentType: "electrical"})
	require.NoError(t, err)
	require.Equal(t, "em-hardened-weak", boosted.Hits[0].FitName)
}

// TestBoostedCompositeArchetype verifies the ×4 archetype ranking boost: with
// equal base Score, a hit whose EFT carries BOTH kite markers (Microwarpdrive
// AND Warp Disruptor — see archetypeMarkers/hasArchetypeModules) outranks an
// otherwise-identical AB-brawler hit once boostSpec.archetype="kite" is set.
func TestBoostedCompositeArchetype(t *testing.T) {
	kiteHit := FitSearchHit{Score: 1, EFT: "[Stabber, k]\n5MN Microwarpdrive II\nWarp Disruptor II"}
	brawlHit := FitSearchHit{Score: 1, EFT: "[Stabber, b]\n1MN Afterburner II\nWarp Scrambler II"}
	spec := boostSpec{archetype: "kite"}
	require.Greater(t, boostedComposite(kiteHit, spec), boostedComposite(brawlHit, spec))
}

// archetypeCostCond distinguishes the strict cost=cheap pass (still carrying
// the facet) from the relaxed one, mirroring tankTypeCostCond — exercises
// archetype sufficiency in isolation via a droppable facet unrelated to
// Activity/WeaponFamily/TankType.
const archetypeCostCond = `"value":"cheap"`

// stabberCostTaggedBrawlOnly is the strict cost=cheap pass result: non-empty
// (pvp-tagged, cheap), but the only hit is an AB brawler — no kite markers
// (Microwarpdrive + Warp Disruptor) anywhere.
const stabberCostTaggedBrawlOnly = `{"result":{"points":[
 {"id":"b","payload":{"ship_name":"Stabber","fit_name":"Cheap AB Brawler","source":"workbench","source_url":"u1","fit_tags":["pvp","cheap"],"views":0,"is_tested":false,"score":0.9,"text":"[Stabber, b]\n1MN Afterburner II\nWarp Scrambler II"}}
]}}`

// stabberRelaxedWithKite is the relaxed (cost facet dropped) result set: a
// stronger AB brawler plus a weaker kite-fitted one, so only the ×4
// archetype boost puts the kite fit on top.
const stabberRelaxedWithKite = `{"result":{"points":[
 {"id":"b2","payload":{"ship_name":"Stabber","fit_name":"brawler-training","source":"workbench","source_url":"u2","fit_tags":["pvp","training"],"views":0,"is_tested":false,"score":0.55,"text":"[Stabber, b2]\n1MN Afterburner II\nWarp Scrambler II"}},
 {"id":"k","payload":{"ship_name":"Stabber","fit_name":"kite-mwd-scram-immune","source":"workbench","source_url":"u3","fit_tags":["pvp"],"views":0,"is_tested":false,"score":0.2,"text":"[Stabber, k]\n5MN Microwarpdrive II\nWarp Disruptor II"}}
]}}`

// TestSearchFitsArchetypeSufficiency covers the archetype sufficiency gate,
// mirroring TestSearchFitsTankTypeSufficiency: the strict pass returns a
// NON-EMPTY, cost-tagged set whose only hit is an AB brawler lacking the
// requested kite markers. Under Archetype="kite" that counts as insufficient
// — the loop keeps relaxing, the dropped facet is reported, and the
// kite-fitted hit from the relaxed pass wins via the ×4 boost (q186: "kite
// fit for low-sec" must not be masked by a shield-buffer AB brawler).
func TestSearchFitsArchetypeSufficiency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), archetypeCostCond) {
			io.WriteString(w, stabberCostTaggedBrawlOnly)
			return
		}
		io.WriteString(w, stabberRelaxedWithKite)
	}))
	defer srv.Close()
	r := NewQdrantRetriever(srv.URL, "c", fakeEmbed{}, 5)

	res, err := r.SearchFits(context.Background(), FitSearchQuery{ShipName: "Stabber", CostClass: "cheap", Archetype: "kite"})
	require.NoError(t, err)
	require.Equal(t, "kite-mwd-scram-immune", res.Hits[0].FitName)
	require.True(t, res.Relaxed)
	require.Equal(t, []string{"cost=cheap"}, res.Dropped)
}
