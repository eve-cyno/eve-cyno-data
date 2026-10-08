package rag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Unit tests for the QC2 PR-2 ranking/filtering stage
// (QC2 analysis §3.2): tackle markers, the negative abyss
// facet, per-pool score normalisation and the archetype filter. They exercise the
// pure functions SearchFits is built from; the golden tests in
// fitsearch_golden_test.go replay recorded corpus payloads through SearchFits.

func TestPvpTackleMarkers_webifierIsNotTackle(t *testing.T) {
	require.NotContains(t, pvpTackleMarkers, "Stasis Webifier")

	for _, eft := range []string{
		"[Stabber, starter]\n10MN Monopropellant Enduring Afterburner\nX5 Enduring Stasis Webifier",
		"[Stabber, web]\nStasis Webifier II",
		"[Stabber, none]\nDamage Control II",
		"",
	} {
		require.False(t, hasTackleModule(eft), "no point in %q", eft)
	}

	// Scrambler/disruptor incl. heavy, faction and meta variants are still tackle.
	for _, eft := range []string{
		"Warp Scrambler II",
		"Warp Disruptor II",
		"Domination Heavy Warp Scrambler",
		"Caldari Navy Warp Disruptor",
		"Faint Epsilon Scoped Warp Scrambler",
		"Heavy Initiated Compact Warp Disruptor",
	} {
		require.True(t, hasTackleModule("[Hull, x]\n"+eft), "%q is a point", eft)
	}
}

func TestPvpTackleBoost_webOnlyFitGetsNoBoost(t *testing.T) {
	webOnly := FitSearchHit{Score: 0.5, EFT: "[Stabber, s]\nX5 Enduring Stasis Webifier"}
	point := FitSearchHit{Score: 0.5, EFT: "[Stabber, p]\nWarp Disruptor II"}
	spec := boostSpec{pvpTackle: true}

	require.InDelta(t, compositeScore(webOnly), boostedComposite(webOnly, spec), 1e-12)
	require.InDelta(t, 4*compositeScore(point), boostedComposite(point, spec), 1e-12)
}

func TestBuildFitSearchMustNot(t *testing.T) {
	abyssCond := QdrantCondition{Key: "fit_tags", Match: map[string]any{"value": "abyss"}}

	tests := []struct {
		name      string
		q         FitSearchQuery
		wantAbyss bool
	}{
		{"pvp", FitSearchQuery{Activity: "pvp"}, true},
		{"hauler", FitSearchQuery{Activity: "hauler"}, true},
		{"exploration", FitSearchQuery{Activity: "exploration"}, true},
		{"mining", FitSearchQuery{Activity: "mining"}, true},
		{"pve without filament/tier", FitSearchQuery{Activity: "pve", ShipName: "Vexor"}, true},
		{"pve + cost facet only", FitSearchQuery{Activity: "pve", CostClass: "cheap"}, true},
		{"activity casing is normalised", FitSearchQuery{Activity: " PvE "}, true},

		{"pve + tier tag", FitSearchQuery{Activity: "pve", Tag: "abyss-t4"}, false},
		{"pve + bare abyss tag", FitSearchQuery{Activity: "pve", Tag: "abyss"}, false},
		{"pve + filament", FitSearchQuery{Activity: "pve", FilamentType: "exotic"}, false},
		{"pve + abyss-only source", FitSearchQuery{Activity: "pve", Source: "caldarijoans"}, false},
		{"pve + abyss word in free text", FitSearchQuery{Activity: "pve", Q: "cheap gila for abyss"}, false},
		{"pve + filament word in free text", FitSearchQuery{Activity: "pve", Q: "exotic filament runner"}, false},
		{"abyss intent beats pvp", FitSearchQuery{Activity: "pvp", Tag: "abyss-t3"}, false},

		{"no activity", FitSearchQuery{ShipName: "Gila"}, false},
		{"activity not in the set", FitSearchQuery{Activity: "abyss"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustNot := buildFitSearchMustNot(tc.q)
			require.Equal(t, quarantinedCondition, mustNot[0], "the G3 quarantine exclusion always rides along")
			if tc.wantAbyss {
				require.Equal(t, []QdrantCondition{quarantinedCondition, abyssCond}, mustNot)
			} else {
				require.Equal(t, []QdrantCondition{quarantinedCondition}, mustNot)
			}
		})
	}
}

// buildFitSearchConditions stays a pure must-facet builder: the abyss exclusion
// lives in must_not and must never become a droppable facet or an essential.
func TestBuildFitSearchConditions_abyssExclusionIsNotAFacet(t *testing.T) {
	essential, facets := buildFitSearchConditions(FitSearchQuery{Activity: "pvp", ShipName: "Stabber"})
	require.Equal(t, []string{"activity=pvp"}, facetLabels(facets))
	for _, c := range essential {
		require.NotEqual(t, "fit_tags", c.Key)
	}
}

func fit(source, name string, score float64, eft string) FitSearchHit {
	return FitSearchHit{ShipName: "Stabber", FitName: name, Source: source, Score: score, EFT: eft}
}

func indexOfFit(hits []FitSearchHit, name string) int {
	for i, h := range hits {
		if h.FitName == name {
			return i
		}
	}
	return -1
}

func TestMeasuredScoreMedian(t *testing.T) {
	require.Zero(t, measuredScoreMedian(nil))
	require.Zero(t, measuredScoreMedian([]FitSearchHit{fit("caldarijoans", "d", 0.5, "")}), "flat defaults are not measurements")
	require.Zero(t, measuredScoreMedian([]FitSearchHit{fit("workbench", "z", 0, "")}), "a missing score is not a measurement")

	odd := []FitSearchHit{fit("workbench", "a", 0.31, ""), fit("workbench", "b", 0.12, ""), fit("workbench", "c", 0.10, ""), fit("caldarijoans", "d", 0.5, "")}
	require.InDelta(t, 0.12, measuredScoreMedian(odd), 1e-12)

	even := append(odd, fit("abysstracker", "e", 0.55, ""))
	require.InDelta(t, (0.12+0.31)/2, measuredScoreMedian(even), 1e-12)
}

// The Stabber pvp defect (§3.2 root cause 2): a caldarijoans default 0.5 outranked
// every measured workbench score (0.10–0.31).
func TestRankFitPool_flatDefaultScoreCannotBeatMeasuredQuality(t *testing.T) {
	pool := func() []FitSearchHit {
		return []FitSearchHit{
			fit("caldarijoans", "default", 0.5, "[Stabber, d]"),
			fit("workbench", "best", 0.31, "[Stabber, a]"),
			fit("workbench", "median", 0.12, "[Stabber, b]"),
			fit("workbench", "floor", 0.10, "[Stabber, c]"),
		}
	}

	// Control: the raw composite ranks the default first — the defect.
	raw := pool()
	require.Greater(t, compositeScore(raw[0]), compositeScore(raw[1]))

	got := rankFitPool(pool(), 10, boostSpec{}, false)
	require.Equal(t, 4, len(got))
	require.Equal(t, "best", got[0].FitName, "measured 0.31 outranks a default 0.5")
	require.Less(t, indexOfFit(got, "median"), indexOfFit(got, "default"), "on a tie with the pool median the measured hit wins")

	// The default is normalised, not rewritten: the stored score is untouched and
	// Relevance is the composite of the capped score.
	d := got[indexOfFit(got, "default")]
	require.InDelta(t, 0.5, d.Score, 1e-12)
	require.InDelta(t, compositeScoreCapped(d, 0.12), d.Relevance, 1e-12)
	require.InDelta(t, compositeScore(got[indexOfFit(got, "median")]), d.Relevance, 1e-12)
}

func TestRankFitPool_capKeepsBoostSemantics(t *testing.T) {
	// A default-valued tackle hit and a measured tackle hit: the ×4 pvp boost
	// applies to both, so the capped default still cannot win.
	pool := []FitSearchHit{
		fit("gustavmannfred", "default-tackle", 0.5, "[Stabber, d]\nWarp Disruptor II"),
		fit("workbench", "measured-tackle", 0.12, "[Stabber, m]\nWarp Scrambler II"),
		fit("workbench", "other", 0.10, "[Stabber, o]\nDamage Control II"),
	}
	got := rankFitPool(pool, 10, boostSpec{pvpTackle: true}, false)
	require.Equal(t, "measured-tackle", got[0].FitName)
	require.InDelta(t, 4*0.12, got[0].Relevance, 1e-9)
	// measured pool = {0.12, 0.10} → median 0.11; the default is capped to it, then ×4.
	require.InDelta(t, 4*0.11, got[indexOfFit(got, "default-tackle")].Relevance, 1e-9)
}

func TestRankFitPool_onlyFlatDefaultsAreNormalised(t *testing.T) {
	pool := []FitSearchHit{
		fit("workbench", "wb-a", 0.20, ""),
		fit("workbench", "wb-b", 0.10, ""),
		fit("abysstracker", "abysstracker-0.55", 0.55, ""), // varied, measured score of a real source
		fit("caldarijoans", "caldarijoans-measured", 0.80, ""),
	}
	got := rankFitPool(pool, 10, boostSpec{}, false)
	require.Equal(t, []string{"caldarijoans-measured", "abysstracker-0.55", "wb-a", "wb-b"}, hitNames(got))
	require.InDelta(t, compositeScore(got[0]), got[0].Relevance, 1e-12, "a non-default score is never capped")
	require.InDelta(t, compositeScore(got[1]), got[1].Relevance, 1e-12)
}

func TestRankFitPool_noMeasuredHitInPoolMeansNoCap(t *testing.T) {
	pool := []FitSearchHit{
		fit("caldarijoans", "a", 0.5, ""),
		fit("gustavmannfred", "b", 0.5, ""),
	}
	got := rankFitPool(pool, 10, boostSpec{}, false)
	for _, h := range got {
		require.InDelta(t, compositeScore(h), h.Relevance, 1e-12)
	}
}

func TestRankFitPool_vectorPathKeepsCosineRelevance(t *testing.T) {
	low := fit("workbench", "low-cosine", 0.9, "")
	low.Relevance = 0.42
	high := fit("caldarijoans", "high-cosine", 0.5, "")
	high.Relevance = 0.91
	got := rankFitPool([]FitSearchHit{low, high}, 10, boostSpec{}, true)
	require.Equal(t, []string{"high-cosine", "low-cosine"}, hitNames(got))
	require.InDelta(t, 0.91, got[0].Relevance, 1e-12)
	require.InDelta(t, 0.42, got[1].Relevance, 1e-12)
}

func TestFilterToArchetype(t *testing.T) {
	kite := fit("workbench", "kite", 0.12, "[Stabber, k]\n5MN Microwarpdrive II\nWarp Disruptor II")
	mwdOnly := fit("workbench", "mwd-only", 0.9, "[Stabber, m]\n5MN Microwarpdrive II")
	brawl := fit("workbench", "brawl", 0.8, "[Stabber, b]\n1MN Afterburner II\nWarp Scrambler II")
	abyss := fit("caldarijoans", "starter", 0.5, "[Stabber, s]\n10MN Afterburner II\nStasis Webifier II")

	t.Run("a matching hit exists: non-matching hits are dropped", func(t *testing.T) {
		got := filterToArchetype([]FitSearchHit{abyss, brawl, mwdOnly, kite}, "kite")
		require.Equal(t, []string{"kite"}, hitNames(got))
	})
	t.Run("brawl keeps scrambler fits", func(t *testing.T) {
		got := filterToArchetype([]FitSearchHit{abyss, kite, brawl}, "brawl")
		require.Equal(t, []string{"brawl"}, hitNames(got))
	})
	t.Run("no hit matches: unchanged (today's behaviour)", func(t *testing.T) {
		in := []FitSearchHit{abyss, brawl, mwdOnly}
		require.Equal(t, in, filterToArchetype(in, "kite"))
	})
	t.Run("no archetype: unchanged", func(t *testing.T) {
		in := []FitSearchHit{abyss, brawl, kite}
		require.Equal(t, in, filterToArchetype(in, ""))
	})
	t.Run("unknown archetype never filters", func(t *testing.T) {
		in := []FitSearchHit{abyss, kite}
		require.Equal(t, in, filterToArchetype(in, "snipe"))
	})
}

func TestRankFitPool_archetypeIsAFilterNotOnlyABoost(t *testing.T) {
	pool := func() []FitSearchHit {
		return []FitSearchHit{
			// Even ×4 on the kite fit (0.12×4 = 0.48) loses to the 0.9 brawler
			// without the filter; with it the brawler must not be returned.
			fit("workbench", "strong-brawler", 0.9, "[Stabber, b]\n1MN Afterburner II\nWarp Scrambler II"),
			fit("workbench", "weak-kite", 0.12, "[Stabber, k]\n5MN Microwarpdrive II\nWarp Disruptor II"),
		}
	}
	for _, useVector := range []bool{false, true} {
		got := rankFitPool(pool(), 10, boostSpec{archetype: "kite"}, useVector)
		require.Equal(t, []string{"weak-kite"}, hitNames(got), "useVector=%v", useVector)
	}

	// No kite candidate at all: both hits stay, ranked as before.
	noKite := []FitSearchHit{
		fit("workbench", "strong-brawler", 0.9, "[Stabber, b]\n1MN Afterburner II\nWarp Scrambler II"),
		fit("workbench", "plain", 0.3, "[Stabber, p]\nDamage Control II"),
	}
	got := rankFitPool(noKite, 10, boostSpec{archetype: "kite"}, false)
	require.Equal(t, []string{"strong-brawler", "plain"}, hitNames(got))
}

func TestRankFitPool_trimsToLimitAfterFilter(t *testing.T) {
	var pool []FitSearchHit
	for _, n := range []string{"k1", "k2", "k3"} {
		pool = append(pool, fit("workbench", n, 0.2, "[Stabber, k]\n5MN Microwarpdrive II\nWarp Disruptor II"))
	}
	pool = append(pool, fit("workbench", "brawl", 0.9, "[Stabber, b]\nWarp Scrambler II"))
	got := rankFitPool(pool, 2, boostSpec{archetype: "kite"}, false)
	require.Len(t, got, 2)
	for _, h := range got {
		require.NotEqual(t, "brawl", h.FitName)
	}
}
