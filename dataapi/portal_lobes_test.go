package dataapi

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
)

// Every tool the catalog offers sits in exactly one lobe of the home page.
func TestPortalLobes_CoverEveryOfferedTool(t *testing.T) {
	placed := map[string]int{}
	for _, l := range portalLobes(offeredTools()) {
		for _, tl := range l.Tools {
			placed[tl.Name]++
		}
	}
	for _, tl := range offeredTools() {
		require.NotZero(t, placed[tl.Name], "%s is offered but sits in no lobe: add it to a lobe in portal_lobes.go", tl.Name)
		require.Equal(t, 1, placed[tl.Name], "%s sits in more than one lobe: keep one entry in portal_lobes.go", tl.Name)
	}
}

// The lobe map names only tools the catalog has, so a retired tool fails here first; a tool
// that is not offered is never rendered.
func TestPortalLobes_NamesOnlyCatalogTools(t *testing.T) {
	known := map[string]catalog.Tool{}
	for _, tl := range catalog.Tools() {
		known[tl.Name] = tl
	}
	for _, spec := range portalLobeSpecs() {
		for _, n := range spec.tools {
			_, ok := known[n]
			require.True(t, ok, "lobe %q names %q, which is not in the catalog: remove it from portal_lobes.go", spec.name, n)
		}
	}
	var offered []catalog.Tool
	for _, tl := range offeredTools() {
		if tl.Name != "analyze_battle" {
			offered = append(offered, tl)
		}
	}
	for _, l := range portalLobes(offered) {
		for _, tl := range l.Tools {
			require.NotEqual(t, "analyze_battle", tl.Name, "a tool that is not offered is rendered")
		}
	}
}

func TestPortalLobes_CountsSumToOffered(t *testing.T) {
	offered := offeredTools()
	lobes := portalLobes(offered)
	var names []string
	sum := 0
	for _, l := range lobes {
		require.NotEmpty(t, l.Tools, "lobe %q is empty", l.Name)
		sum += len(l.Tools)
		names = append(names, l.Name)
	}
	require.Equal(t, len(offered), sum)
	require.Equal(t, "Fits and ships, Items and market, Industry, Navigation, Universe, Combat", strings.Join(names, ", "))
}

// The tally follows the Hall of Fame list, not a literal.
func TestPortalCreditTally_MatchesCredits(t *testing.T) {
	cs := credits()
	links := 0
	byCat := map[string]int{}
	for _, c := range cs {
		links += len(c.Links)
		byCat[c.Cat]++
	}
	got := tallyCredits(cs)
	require.Equal(t, len(cs), got.Projects)
	require.Equal(t, links, got.Links)
	require.Equal(t, len(byCat), got.Kinds)
	require.Equal(t, byCat, got.ByCat)
	// today's fixture
	require.Equal(t, 15, got.Projects)
	require.Equal(t, 17, got.Links)
	require.Equal(t, 4, got.Kinds)
	require.Equal(t, map[string]int{"game": 2, "community": 7, "tool": 2, "oss": 4}, got.ByCat)
}

// Every data-slot of the home design has a value on the home view, equal to the config or
// the catalog it comes from.
func TestPortalHomeView_HasEverySlotValue(t *testing.T) {
	s := PublicSurface()
	d := newPageData(portalInput{Surface: s, HaveSDE: true}, "v")
	offered := offeredTools()
	var pub, keyed, live int
	for _, tl := range offered {
		if tl.Tier == catalog.TierPublic {
			pub++
		} else {
			keyed++
		}
		if tl.Upstream {
			live++
		}
	}
	require.Equal(t, len(offered), d.Stats.Offered)
	require.Equal(t, pub, d.Stats.Public)
	require.Equal(t, live, d.Stats.Live) // tools_upstream
	require.NotZero(t, d.Stats.Live)
	require.Equal(t, "ok", d.Health)

	l := s.Limits
	require.Equal(t, l.MCP.Max, d.Lim.MCP)                   // limit_mcp
	require.Equal(t, s.keyedRate(l.MCP).Max, d.KeyLim.MCP)   // limit_mcp_key
	require.Equal(t, l.UpstreamTool.Max, d.Lim.UpstreamTool) // limit_upstream
	require.Equal(t, s.keyedRate(l.UpstreamTool).Max, d.KeyLim.UpstreamTool)
	require.Equal(t, l.Tool.Max, d.Lim.Tool) // limit_rest
	require.Equal(t, s.keyedRate(l.Tool).Max, d.KeyLim.Tool)
	require.Equal(t, l.FitsSearch.Max, d.Lim.FitsSearch) // limit_fits
	require.Equal(t, s.keyedRate(l.FitsSearch).Max, d.KeyLim.FitsSearch)
	require.Equal(t, l.ItemsSearch.Max, d.Lim.ItemsSearch) // limit_items
	require.Equal(t, s.keyedRate(l.ItemsSearch).Max, d.KeyLim.ItemsSearch)
	for name, v := range map[string]int{"mcp": d.Lim.MCP, "upstream": d.Lim.UpstreamTool, "rest": d.Lim.Tool, "fits": d.Lim.FitsSearch, "items": d.Lim.ItemsSearch, "mcp key": d.KeyLim.MCP, "fit max": d.Stats.FitMax} {
		require.Positive(t, v, name)
	}

	require.Equal(t, thousands(s.KeyDailyCap), d.KeyCap)   // limit_key_day
	require.Equal(t, thousands(s.AnonDailyCap), d.AnonCap) // limit_anon_day
	require.NotEmpty(t, d.KeyCap)
	require.NotEmpty(t, d.AnonCap)
	require.Equal(t, "10,000", d.KeyCap)

	require.Equal(t, strconv.Itoa(pub)+" "+plural(pub, "tool"), d.ToolsPublicLabel)
	require.Equal(t, strconv.Itoa(keyed)+" "+plural(keyed, "tool"), d.ToolsKeyedLabel) // tools_keyed_label

	require.Len(t, d.Lobes, 6) // lobe_total
	sum := 0
	for _, lb := range d.Lobes {
		sum += len(lb.Tools) // lobe_count
	}
	require.Equal(t, len(offered), sum)

	require.NotEmpty(t, d.KeyedNames) // keys_note branches on Auth, SelfServe and Tools.Keyed
	require.Equal(t, 15, d.CreditTally.Projects)
}
