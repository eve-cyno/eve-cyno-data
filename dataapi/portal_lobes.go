package dataapi

import "eve-cyno.dev/go/data/catalog"

// portalLobe is one group of the tool list on the home page.
type portalLobe struct {
	Name  string
	Tools []catalog.Tool
	// Rows are the same tools as the page's tool rows: the tier and the live label the
	// atlas marks them with.
	Rows []toolRowView
}

type portalLobeSpec struct {
	name  string
	tools []string
}

// portalLobeSpecs is the one place that says which lobe of the home page a tool sits in, in
// the order the page lists them. A tool the catalog offers but this map does not place fails
// TestPortalLobes_CoverEveryOfferedTool: add its name to a lobe here.
func portalLobeSpecs() []portalLobeSpec {
	return []portalLobeSpec{
		{"Fits and ships", []string{
			"get_ship_stats",
			"get_ship_bonuses",
			"get_hull_facts",
			"find_canonical_module",
			"get_fits",
			"list_fits",
			"validate_fitting",
			"compute_fit_stats",
			"get_required_skills",
		}},
		{"Items and market", []string{
			"search_item_by_name",
			"get_type_info",
			"get_market_price",
			"get_average_prices",
			"get_market_history",
			"appraise_items",
		}},
		{"Industry", []string{
			"get_production_chain",
			"get_reprocessing_yield",
			"get_lp_offers",
		}},
		{"Navigation", []string{
			"get_jumps_between",
			"get_jump_distances",
			"plan_jump_route",
			"get_thera_connections",
			"get_npc_stations",
		}},
		{"Universe", []string{
			"get_systems_in_region",
			"find_systems",
			"get_system_info",
			"get_sovereignty",
			"get_system_activity",
			"get_activity_baseline",
			"compare_activity_to_baseline",
		}},
		{"Combat", []string{
			"get_recent_kills",
			"analyze_battle",
		}},
	}
}

// portalLobes groups the offered tools into the lobes of the home page, in the order of the
// map. A tool that is not offered is not listed; a lobe with no offered tool is dropped.
func portalLobes(offered []catalog.Tool) []portalLobe {
	byName := make(map[string]catalog.Tool, len(offered))
	for _, t := range offered {
		byName[t.Name] = t
	}
	var out []portalLobe
	for _, spec := range portalLobeSpecs() {
		l := portalLobe{Name: spec.name}
		for _, n := range spec.tools {
			if t, ok := byName[n]; ok {
				l.Tools = append(l.Tools, t)
				l.Rows = append(l.Rows, rowOf(t))
			}
		}
		if len(l.Tools) > 0 {
			out = append(out, l)
		}
	}
	return out
}
