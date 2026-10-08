package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
)

// Deps holds the runtime dependencies for tool execution.
type Deps struct {
	SDE       *sde.SDE
	Client    *Client
	Retriever *rag.QdrantRetriever // nil (SDE-only mode) → get_fits / list_fits say the corpus is not configured
	// JaniceAPIKey is set from config for appraise_items.
	JaniceAPIKey string
	// Stats is the one dogma engine behind compute_fit_stats and the validate_fitting
	// stat card, built once over SDE where the Deps are (core/bootstrap) so every call
	// reuses its warm SDE memo (a cold engine reads ~2x slower). nil → StatsEngine
	// builds one lazily, once (tests, struct literals).
	Stats *gofa.Engine

	// Legality is the one Alpha-clone legality checker (skill allowlist + meta tier)
	// behind validate_fitting and the fit controller, built once where the Deps are
	// (core/bootstrap). nil → AlphaLegality builds one lazily, once (tests, struct
	// literals); nil there too when the allowlist cannot be loaded.
	Legality *fit.Legality

	statsOnce sync.Once    // guards statsLazy
	statsLazy *gofa.Engine // StatsEngine's fallback when Stats is nil

	legalityOnce sync.Once     // guards legalityLazy
	legalityLazy *fit.Legality // AlphaLegality's fallback when Legality is nil
}

// WithJaniceKey returns a copy of d that appraises with key instead of d.JaniceAPIKey
// (key "" leaves Janice unconfigured: appraise_items then says so). d is not modified, so
// a public surface can give each request its own caller's key without a data race and
// without the project key ever being in reach. The copy shares d's SDE, HTTP client,
// retriever and the (lazily built) stats engine and legality checker, so it answers as warm
// as d does. A nil d stays nil.
func (d *Deps) WithJaniceKey(key string) *Deps {
	if d == nil {
		return nil
	}
	return &Deps{
		SDE:          d.SDE,
		Client:       d.Client,
		Retriever:    d.Retriever,
		JaniceAPIKey: key,
		Stats:        d.StatsEngine(),
		Legality:     d.AlphaLegality(),
	}
}

// dispatch runs the named tool and returns its LLM-oriented text and, for the tools
// that have one, the typed result built from the same values (nil otherwise).
// ExecuteToolResult wraps it and ExecuteTool wraps that (result.go), so the text cannot
// differ between the chat path and the public API.
func dispatch(ctx context.Context, deps *Deps, name string, arguments map[string]any) (string, any, error) {
	switch name {
	case "get_jumps_between":
		from, _ := arguments["from_system"].(string)
		to, _ := arguments["to_system"].(string)
		preferHS, _ := arguments["prefer_high_sec"].(bool)
		return plain(getJumpsBetween(deps.SDE, from, to, preferHS))

	case "get_systems_in_region":
		region, _ := arguments["region_name"].(string)
		secMin := floatArg(arguments, "sec_min", -1.0)
		secMax := floatArg(arguments, "sec_max", 1.0)
		return plain(getSystemsInRegion(deps.SDE, region, secMin, secMax))

	case "get_npc_stations":
		system, _ := arguments["system_name"].(string)
		return plain(getNPCStations(deps.SDE, system))

	case "get_reprocessing_yield":
		// Schema param is "quantity" (NOT "runs"); reading "runs" dropped the
		// user's quantity factor → yields off by that multiplier.
		item, _ := arguments["item_name"].(string)
		quantity := intArg(arguments, "quantity", 1)
		if quantity == 0 {
			quantity = 1 // mirror Python `int(... or 1)`
		}
		eff := floatArg(arguments, "refining_efficiency_pct", 50.0)
		return plain(getReprocessingYield(deps.SDE, item, quantity, eff))

	case "get_required_skills":
		// Schema param "purpose" selects fly (default) vs build prereqs.
		item, _ := arguments["item_name"].(string)
		purpose, _ := arguments["purpose"].(string)
		if purpose == "" {
			purpose = "fly"
		}
		return plain(getRequiredSkills(deps.SDE, item, purpose))

	case "get_production_chain":
		// Python defaults me_level to 10 (fully-researched BPO) — the mineral
		// totals graders assume ME10. Also reads the optional structure-rig /
		// cost-index / industry-skills params the schema exposes.
		item, _ := arguments["item_name"].(string)
		runs := intArg(arguments, "runs", 1)
		if runs == 0 {
			runs = 1 // mirror Python `int(... or 1)`
		}
		me := intArg(arguments, "me_level", 10)
		stationRig := intArg(arguments, "station_rig_level", 0)
		costIndex := floatArg(arguments, "system_cost_index", 0.0)
		peSkill := 0
		if skillsArg, ok := arguments["industry_skills"].(map[string]any); ok {
			peSkill = intArg(skillsArg, "production_efficiency", 0)
		}
		return plain(getProductionChain(deps.SDE, item, runs, me, stationRig, costIndex, peSkill))

	case "get_hull_facts":
		ship, _ := arguments["ship_name"].(string)
		return plain(getHullFacts(ctx, deps.SDE, deps.Retriever, ship))

	case "find_canonical_module":
		family, _ := arguments["family"].(string)
		size, _ := arguments["size"].(string)
		var sizePtr *string
		if size != "" {
			sizePtr = &size
		}
		return plain(findCanonicalModule(deps.SDE, family, sizePtr))

	case "validate_fitting":
		// Schema param is "eft_text"; the guards call it internally with "eft_block".
		// Reading only "eft_block" dropped the EFT when the MODEL self-validated a fit
		// → empty validator input → fits slipped through / hard-blocked (fit_generation).
		// A valid fit validated at the MODEL's request (eft_text) also gets the stat
		// card; the guards' eft_block calls do not (see validateFittingWithStats).
		return typed(validateFittingWithStats(ctx, deps, arguments))

	case "compute_fit_stats":
		text, card := computeFitStats(ctx, deps, arguments)
		return typed(text, card, nil)

	case "analyze_battle":
		// Schema param is "battle_url" (not "url"); reading the wrong key dropped the
		// URL → "No battle report URL provided." for every battle question.
		url, _ := arguments["battle_url"].(string)
		if url == "" {
			url, _ = arguments["url"].(string) // tolerate the alias
		}
		detail, _ := arguments["detail"].(string)
		if detail == "" {
			detail = "summary"
		}
		return plainErr(analyzeBattle(ctx, deps.Client, deps.SDE, url, detail))

	case "get_fits":
		return typed(getFits(ctx, deps, arguments))

	case "list_fits":
		return typed(listFits(ctx, deps, arguments))

	case "get_market_price":
		typeID := intArg(arguments, "type_id", 0)
		if typeID == 0 {
			return plain("type_id required for get_market_price")
		}
		return typed(getMarketPrice(ctx, deps.Client, deps.SDE, typeID))

	case "get_type_info":
		typeID := intArg(arguments, "type_id", 0)
		return typed(getTypeInfo(ctx, deps.Client, deps.SDE, typeID))

	case "get_ship_stats":
		ship, _ := arguments["ship_name"].(string)
		return typed(getShipStats(ctx, deps.Client, deps.SDE, ship))

	case "get_ship_bonuses":
		// get_ship_bonuses is similar to get_ship_stats — use hull_facts internally.
		ship, _ := arguments["ship_name"].(string)
		return plain(getHullFacts(ctx, deps.SDE, deps.Retriever, ship))

	case "search_item_by_name":
		// Schema param "names" is an ARRAY of strings; reading it as a single string
		// dropped every query (→ the model could not resolve module names → hallucinated).
		names := stringSlice(arguments["names"])
		if len(names) == 0 {
			names = stringSlice(arguments["name"]) // tolerate singular alias
		}
		return plainErr(searchItemByName(ctx, deps.Client, deps.SDE, names))

	case "appraise_items":
		items, _ := arguments["items"].(string)
		return plainErr(appraiseItems(ctx, deps.Client, items, deps.JaniceAPIKey))

	case "get_sovereignty":
		system, _ := arguments["system_name"].(string)
		return plainErr(getSovereignty(ctx, deps.Client, deps.SDE, system))

	case "get_system_activity":
		system, _ := arguments["system_name"].(string)
		return plainErr(getSystemActivity(ctx, deps.Client, deps.SDE, system))

	case "convert_isk_to_real":
		amount := floatArg(arguments, "isk_amount", 0)
		currency, _ := arguments["currency"].(string)
		if currency == "" {
			currency = "BOTH"
		}
		return plainErr(convertISKToReal(ctx, deps.Client, amount, currency))

	default:
		return "", nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func floatArg(args map[string]any, key string, def float64) float64 {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	}
	return def
}

// stringSlice coerces a tool argument into []string. JSON decoding yields []any
// for an array argument; some models also pass a single string or a comma-joined
// string. Empty entries are dropped.
func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	}
	return def
}
