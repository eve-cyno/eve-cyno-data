package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
	// JaniceAPIKey, when set, makes appraise_items a Janice appraisal; empty means the
	// estimate from ESI's average prices. Set from config; a public surface replaces it with
	// the caller's own key (ForPublic).
	JaniceAPIKey string
	// Public marks answers that go to a reader other than the chat brain (the public REST and
	// MCP surfaces, a bot post): tool texts leave out the chat loop's instructions and
	// get_ship_bonuses answers the bonus section only. Typed data does not change. The zero
	// value is the chat brain's text, which the golden tests pin.
	Public bool
	// Stats is the one dogma engine behind compute_fit_stats and the validate_fitting
	// stat card, built once over SDE where the Deps are (bootstrap) so every call
	// reuses its warm SDE memo (a cold engine reads ~2x slower). nil → StatsEngine
	// builds one lazily, once (tests, struct literals).
	Stats *gofa.Engine

	// Legality is the one Alpha-clone legality checker (skill allowlist + meta tier)
	// behind validate_fitting and the fit controller, built once where the Deps are
	// (bootstrap). nil → AlphaLegality builds one lazily, once (tests, struct
	// literals); nil there too when the allowlist cannot be loaded.
	Legality *fit.Legality

	statsOnce sync.Once    // guards statsLazy
	statsLazy *gofa.Engine // StatsEngine's fallback when Stats is nil

	legalityOnce sync.Once     // guards legalityLazy
	legalityLazy *fit.Legality // AlphaLegality's fallback when Legality is nil
}

// ForPublic returns a copy of d for a public surface: Public set and the Janice key = janiceKey
// (key "" means no Janice: appraise_items then answers with the ESI estimate). d is not modified, so
// a public surface can give each request its own caller's key without a data race and
// without the project key ever being in reach. The copy shares d's SDE, HTTP client,
// retriever and the (lazily built) stats engine and legality checker, so it answers as warm
// as d does. A nil d stays nil.
func (d *Deps) ForPublic(janiceKey string) *Deps {
	if d == nil {
		return nil
	}
	return &Deps{
		SDE:          d.SDE,
		Client:       d.Client,
		Retriever:    d.Retriever,
		JaniceAPIKey: janiceKey,
		Public:       true,
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
		detail, _ := arguments["detail"].(string)
		text, route := getJumpsBetween(deps.SDE, from, to, RouteRequest{
			PreferHighSec: preferHS, Detail: detail,
			Avoid: stringSlice(arguments["avoid"]), Via: stringSlice(arguments["via"]),
		})
		return typed(text, route, nil)

	case "get_jump_distances":
		origin, _ := arguments["origin"].(string)
		preferHS, _ := arguments["prefer_high_sec"].(bool)
		text, dist := getJumpDistances(deps.SDE, origin, stringSlice(arguments["systems"]), preferHS, stringSlice(arguments["avoid"]))
		return typed(text, dist, nil)

	case "plan_jump_route":
		return typed(planJumpRoute(ctx, deps.Client, deps.SDE, arguments))

	case "get_average_prices":
		typeIDs, badIDs := intSlice(arguments["type_ids"])
		return typed(getAveragePrices(ctx, deps.Client, deps.SDE, stringSlice(arguments["names"]), typeIDs, badIDs))

	case "find_systems":
		text, list := findSystems(deps.SDE, arguments)
		return typed(text, list, nil)

	case "get_system_info":
		text, info := getSystemInfo(deps.SDE, stringSlice(arguments["systems"]))
		return typed(text, info, nil)

	case "get_thera_connections":
		return typed(getTheraConnections(ctx, deps.Client, deps.SDE, arguments))

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
		// Absent quantity = one batch (getReprocessingYield treats 0 as that);
		// an explicit value below 1 is rejected.
		quantity := 0
		if raw, ok := arguments["quantity"]; ok && raw != nil && !blankString(raw) {
			quantity = cappedIntArg(arguments, "quantity", 0)
			if f, isNum := numberArg(raw); isNum && f > maxReprocessQuantity {
				return plain(fmt.Sprintf("quantity: 1 to %d (got %.0f).", maxReprocessQuantity, f))
			}
			if quantity < 1 {
				return plain(fmt.Sprintf("quantity: at least 1 (got %d).", quantity))
			}
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
		runs := 1 // absent runs = one run
		if raw, ok := arguments["runs"]; ok && raw != nil && !blankString(raw) {
			runs = cappedIntArg(arguments, "runs", 1)
			if runs < 1 || runs > maxProductionRuns {
				return plain(rangeError("runs", 1, maxProductionRuns, runs))
			}
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
		return plain(getHullFacts(ctx, deps.SDE, deps.Retriever, ship, deps.Public))

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
		hub, _ := arguments["hub"].(string)
		return typed(getMarketPriceFor(ctx, deps.Client, deps.SDE, typeID, hub, deps.Public))

	case "get_market_history":
		itemName, _ := arguments["item_name"].(string)
		hub, _ := arguments["hub"].(string)
		region, _ := arguments["region"].(string)
		bucket, _ := arguments["bucket"].(string)
		days := 0 // absent: the default
		if raw, ok := arguments["days"]; ok && raw != nil && !blankString(raw) {
			if days = cappedIntArg(arguments, "days", 0); days == 0 {
				return plain(rangeError("days", 1, maxHistoryDays, 0))
			}
		}
		return typed(getMarketHistory(ctx, deps.Client, deps.SDE, marketHistoryRequest{
			TypeID: intArg(arguments, "type_id", 0), ItemName: itemName, Hub: hub, Region: region,
			Days: days, Bucket: bucket, Public: deps.Public,
		}))

	case "get_type_info":
		typeID := intArg(arguments, "type_id", 0)
		return typed(getTypeInfoFor(ctx, deps.Client, deps.SDE, typeID, deps.Public))

	case "get_ship_stats":
		ship, _ := arguments["ship_name"].(string)
		return typed(getShipStats(ctx, deps.Client, deps.SDE, ship))

	case "get_ship_bonuses":
		// get_ship_bonuses is similar to get_ship_stats — use hull_facts internally.
		ship, _ := arguments["ship_name"].(string)
		if deps.Public {
			return plain(getShipBonuses(deps.SDE, ship))
		}
		return plain(getHullFacts(ctx, deps.SDE, deps.Retriever, ship, false))

	case "search_item_by_name":
		// Schema param "names" is an ARRAY of strings; reading it as a single string
		// dropped every query (→ the model could not resolve module names → hallucinated).
		names := stringSlice(arguments["names"])
		if len(names) == 0 {
			names = stringSlice(arguments["name"]) // tolerate singular alias
		}
		return plainErr(searchItemByName(ctx, deps.Client, deps.SDE, names, deps.Public))

	case "appraise_items":
		items, _ := arguments["items"].(string)
		return plainErr(appraiseItems(ctx, deps.Client, deps.SDE, items, deps.JaniceAPIKey))

	case "get_sovereignty":
		system, _ := arguments["system_name"].(string)
		if systems := batchSystems(system, arguments["systems"]); len(systems) > 0 {
			return typed(getSovereignties(ctx, deps.Client, deps.SDE, systems))
		}
		if strings.TrimSpace(system) == "" {
			return plain(fmt.Sprintf("systems: give 1 to %d system names.", maxSystemsBatch))
		}
		return plainErr(getSovereignty(ctx, deps.Client, deps.SDE, system))

	case "get_system_activity":
		system, _ := arguments["system_name"].(string)
		if systems := batchSystems(system, arguments["systems"]); len(systems) > 0 {
			return typed(getSystemActivities(ctx, deps.Client, deps.SDE, systems))
		}
		if strings.TrimSpace(system) == "" {
			return plain(fmt.Sprintf("systems: give 1 to %d system names.", maxSystemsBatch))
		}
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

// intSlice coerces a tool argument into []int the way intArg coerces one number: JSON
// numbers, json.Number and numeric strings are accepted, and so is a single value.
// Every other entry (not a whole number, not numeric) is returned as bad, in its
// printed form, so the caller can report it instead of dropping it.
func intSlice(v any) (ids []int, bad []string) {
	one := func(e any) {
		switch n := e.(type) {
		case int:
			ids = append(ids, n)
			return
		case int64:
			ids = append(ids, int(n))
			return
		case float64:
			if n == float64(int(n)) {
				ids = append(ids, int(n))
				return
			}
		case json.Number:
			if i, err := n.Int64(); err == nil {
				ids = append(ids, int(i))
				return
			}
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				ids = append(ids, i)
				return
			}
		}
		if e == nil {
			bad = append(bad, "null")
			return
		}
		bad = append(bad, fmt.Sprint(e))
	}
	switch x := v.(type) {
	case nil:
	case []int:
		ids = append(ids, x...)
	case []any:
		for _, e := range x {
			one(e)
		}
	default:
		one(v)
	}
	return ids, bad
}

func intArg(args map[string]any, key string, def int) int {
	return intArgWith(args, key, def, floatToInt)
}

// cappedIntArg reads an integer parameter that has a range check. It is intArg, except a
// number outside the int range saturates instead of falling back to def, so the caller's
// range check answers the parameter's range line rather than the default being used silently.
func cappedIntArg(args map[string]any, key string, def int) int {
	return intArgWith(args, key, def, floatToIntSaturating)
}

func intArgWith(args map[string]any, key string, def int, conv func(float64, int) int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return conv(val, def)
	case int64:
		return int(val)
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return int(i)
		}
		if f, err := val.Float64(); err == nil {
			return conv(f, def)
		}
	case string:
		// The chat's text tool-call fallback keeps quoted values as strings.
		t := strings.TrimSpace(val)
		if i, err := strconv.Atoi(t); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return conv(f, def)
		}
	}
	return def
}

// floatToInt truncates f to an int; a non-finite value or one outside the int range gives def
// (a float that large would convert to an arbitrary integer).
func floatToInt(f float64, def int) int {
	if math.IsNaN(f) || math.IsInf(f, 0) || f >= math.MaxInt64 || f <= math.MinInt64 {
		return def
	}
	return int(f)
}

// floatToIntSaturating truncates f to an int; a value outside the int range (or infinite)
// becomes the nearest bound, and NaN the lowest int, so a range check always rejects it.
func floatToIntSaturating(f float64, _ int) int {
	switch {
	case math.IsNaN(f) || f <= math.MinInt64:
		return math.MinInt
	case f >= math.MaxInt64:
		return math.MaxInt
	}
	return int(f)
}

// numberArg reads a numeric argument as a float for messages; ok is false for anything that
// is not a number or a numeric string.
func numberArg(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return f, err == nil
	}
	return 0, false
}

// blankString reports whether v is a string holding only white space (an absent value).
func blankString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// batchSystems is the systems list of a batch call; a system_name given beside it is
// prepended. Empty means the older single-system form.
func batchSystems(single string, list any) []string {
	systems := stringSlice(list)
	if len(systems) == 0 {
		return nil
	}
	if strings.TrimSpace(single) != "" {
		systems = append([]string{single}, systems...)
	}
	return systems
}
