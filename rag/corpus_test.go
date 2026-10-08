package rag

import (
	"testing"

	"eve-cyno.dev/go/data/corpus"
	"github.com/stretchr/testify/require"
)

// TestFilterKeysAreDeclaredInCorpus: every payload key a rag filter conditions on is
// a key core/corpus declares, for the widest query each filter builder accepts. The
// write-side counterpart (the writers emit only declared keys, and what they emit
// matches these filters) is the contract test in ingest/corpuscontract.
func TestFilterKeysAreDeclaredInCorpus(t *testing.T) {
	q := FitSearchQuery{
		Activity: corpus.TagPvE, Tag: corpus.TagAbyss, ShipName: "Gila", FilamentType: corpus.FilamentExotic,
		CostClass: corpus.TagCheap, Source: corpus.SourceWorkbench, Clone: "alpha",
	}
	essential, facets := buildFitSearchConditions(q)
	conds := append(essential, buildFitSearchMustNot(q)...)
	for _, f := range facets {
		conds = append(conds, f.cond)
	}
	for _, c := range conds {
		require.Truef(t, corpus.IsKey(c.Key), "SearchFits filters on %q, which core/corpus does not declare", c.Key)
	}
	require.True(t, corpus.IsKey(quarantinedCondition.Key))

	list := buildListFitFilter(ListFitQuery{Source: corpus.SourceWorkbench, ShipName: "Gila", Tag: corpus.TagPvP, FilamentType: corpus.FilamentDark})
	list = append(list, quarantinedMustNot)
	for _, c := range list {
		require.Truef(t, corpus.IsKey(c["key"].(string)), "list_fits filters on %q, which core/corpus does not declare", c["key"])
	}
}

// TestFitSourceListsAreCorpusSources: the reader's default source sets only name
// sources core/corpus declares.
func TestFitSourceListsAreCorpusSources(t *testing.T) {
	for _, list := range [][]string{fitSearchSources, defaultFitSources, defaultListFitSources} {
		for _, s := range list {
			require.Contains(t, corpus.FitSources(), s)
		}
	}
	for s := range AbyssSourcePriority {
		require.Contains(t, corpus.FitSources(), s)
	}
	for tag := range AbyssFitTags {
		require.Truef(t, corpus.IsFitTag(tag), "AbyssFitTags names %q outside the corpus tag vocabulary", tag)
	}
	for fil := range filamentExpectedHardeners {
		require.Contains(t, corpus.Filaments(), fil)
	}
}
