package tools

import (
	"slices"

	"eve-cyno.dev/go/data/corpus"
	"eve-cyno.dev/go/data/rag"
)

// The upstreams the tools draw on. License is a short statement of the terms the data
// is used under; the upstream terms of use are the authoritative record behind
// every entry (re-check the two when a source changes).
var (
	sourceESI = Source{
		Name:    "ESI (EVE Swagger Interface)",
		URL:     "https://esi.evetech.net/",
		License: "EVE Developer License Agreement (Fenris Creations); non-commercial use",
	}
	sourceSDE = Source{
		Name:    "EVE Static Data Export (via Fuzzwork)",
		URL:     "https://www.fuzzwork.co.uk/dump/",
		License: "Fenris Creations game data under the EVE Developer License Agreement; converted by Fuzzwork Enterprises",
	}
	sourceEVEUniversityIce = Source{
		Name:    "EVE University Wiki (ice belt systems)",
		URL:     "https://wiki.eveuniversity.org/Ice_harvesting",
		License: "CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/); credit EVE University; the dataset stays CC BY-SA",
	}
	sourceEveScout = Source{
		Name:    "EvE-Scout / Signal Cartel",
		URL:     "https://www.eve-scout.com/",
		License: "Public API without published terms, used with attribution; live scanning data by Signal Cartel",
	}
	sourceZKillboard = Source{
		Name:    "zKillboard",
		URL:     "https://zkillboard.com/",
		License: fitLicense(corpus.SourceZkillboardMeta),
	}
	sourceWarBeacon = Source{
		Name:    "WarBeacon",
		URL:     "https://warbeacon.net/",
		License: "No licence published; one report per user-supplied URL",
	}
	sourceEveTools = Source{
		Name:    "br.evetools.org",
		URL:     "https://br.evetools.org/",
		License: "No licence published; one report per user-supplied URL",
	}
	sourceJanice = Source{
		Name:    "Janice",
		URL:     "https://janice.e-351.com/",
		License: "Private API used with a key granted by its creator",
	}
	sourceFrankfurter = Source{
		Name:    "Frankfurter",
		URL:     "https://frankfurter.dev/",
		License: "Open-source service; exchange rates from central banks and other official sources",
	}
	sourceWorkbench = Source{
		Name:    "EVE Workbench",
		URL:     "https://eveworkbench.com/",
		License: fitLicense(corpus.SourceWorkbench),
	}
	sourceAbysstracker = Source{
		Name:    "Abysstracker",
		URL:     "https://abysstracker.com/",
		License: fitLicense(corpus.SourceAbyssTracker),
	}
	sourceGustavmannfred = Source{
		Name:    "gustavmannfred (abyss fits)",
		URL:     "https://gustavmannfred.streamlit.app/",
		License: fitLicense(corpus.SourceGustavmannfred),
	}
	sourceCaldarijoans = Source{
		Name:    "caldarijoans (abyss fits)",
		URL:     "https://caldarijoans.streamlit.app/",
		License: fitLicense(corpus.SourceCaldarijoans),
	}
)

// fitLicense is the licence statement of a corpus fit source. The per-hit attribution
// (rag.Attribution) reads the same table, so a tool's Source and a hit's licence
// cannot disagree.
func fitLicense(key string) string {
	l, _ := rag.SourceLicenseOf(key)
	return l.License
}

// communityFits are the corpus sources behind get_fits / list_fits (corpus.FitSources).
var communityFits = []Source{sourceWorkbench, sourceAbysstracker, sourceGustavmannfred, sourceCaldarijoans, sourceZKillboard}

// toolAttribution is the single table of what each tool's result is derived from. A
// tool in tool_schemas.json without an entry fails TestAttribution_CoversEverySchemaTool.
var toolAttribution = map[string][]Source{
	// SDE only.
	"get_jumps_between":      {sourceSDE},
	"get_jump_distances":     {sourceSDE},
	"get_systems_in_region":  {sourceSDE},
	"get_npc_stations":       {sourceSDE},
	"find_systems":           {sourceSDE, sourceEVEUniversityIce},
	"get_system_info":        {sourceSDE, sourceEVEUniversityIce},
	"get_reprocessing_yield": {sourceSDE},
	"get_required_skills":    {sourceSDE},
	"find_canonical_module":  {sourceSDE},
	"search_item_by_name":    {sourceSDE},
	"get_ship_stats":         {sourceSDE},
	"compute_fit_stats":      {sourceSDE},

	// SDE plus an example community fit from EVE Workbench when the corpus is up.
	"get_hull_facts":   {sourceSDE, sourceWorkbench},
	"get_ship_bonuses": {sourceSDE, sourceWorkbench},

	// SDE, with ESI as the fallback for names and dogma the SDE lacks (validate_fitting)
	// or as the live data (sovereignty, kills).
	"validate_fitting":    {sourceSDE, sourceESI},
	"get_type_info":       {sourceSDE, sourceESI},
	"get_sovereignty":     {sourceSDE, sourceESI},
	"get_system_activity": {sourceSDE, sourceESI},
	"plan_jump_route":     {sourceSDE, sourceESI}, // ESI only when avoid_incursions or live_info is set
	// ESI only with system (cost index) or include_cost (the daily price list).
	"get_production_chain": {sourceSDE, sourceESI},

	// ESI.
	"get_market_price": {sourceESI},

	// ESI's price list, item names from the SDE.
	"get_average_prices": {sourceESI, sourceSDE},
	"get_market_history": {sourceESI, sourceSDE},

	// Live Thera and Turnur connections from EvE-Scout; security, region and distances from the SDE.
	"get_thera_connections": {sourceEveScout, sourceSDE},

	// The community fit corpus.
	"get_fits":  communityFits,
	"list_fits": communityFits,

	// Battle reports: the user's URL picks the backend; ESI adds killmail detail and the
	// SDE names ships and systems.
	"analyze_battle": {sourceZKillboard, sourceWarBeacon, sourceEveTools, sourceESI, sourceSDE},

	// The estimate from ESI's average prices over names resolved in the SDE; Janice with a
	// Janice key.
	"appraise_items":      {sourceESI, sourceSDE, sourceJanice},
	"convert_isk_to_real": {sourceESI, sourceFrankfurter}, // PLEX price from ESI, USD/EUR from Frankfurter
}

// IsEmbeddedSource reports whether a source is data shipped with the binary (the SDE build,
// the ice dataset): a tool with only such sources does not reach the outside world.
func IsEmbeddedSource(src Source) bool {
	return src.Name == sourceSDE.Name || src.Name == sourceEVEUniversityIce.Name
}

// Attribution returns the upstreams a tool's result is derived from, or nil for a name
// that is not a tool. The slice is the caller's to keep.
func Attribution(tool string) []Source {
	return slices.Clone(toolAttribution[tool])
}
