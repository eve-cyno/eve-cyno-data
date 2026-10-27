// Package catalog is the one tool registry the generated public surfaces read: the
// OpenAPI document, llms.txt / llms-full.txt, the REST tool endpoint and the MCP server.
// It joins what tools already owns, split across three places, into one
// value per tool:
//
//   - tools/tool_schemas.json: the name, the description (written as a prompt for the
//     model, so it is also the best description a human API reader gets) and the JSON
//     Schema of the arguments;
//   - tools.Attribution: the upstreams behind each answer;
//   - the public-surface policy below: the access tier of each tool (who may run it on
//     an internet-facing surface), and which typed result a tool carries next to its text.
//
// Nothing here executes a tool; execution stays tools.ExecuteToolResult. The package
// imports only tools, so a surface can depend on it without a path to anything else.
package catalog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"

	"eve-cyno.dev/go/data/tools"
)

// Tool is one entry of the registry. Parameters is shared with tools.ToolSchemas: callers
// must treat it (and everything below it) as read-only.
type Tool struct {
	// Name is the tool name as called (also the last segment of POST /v1/tool/{name}).
	Name string
	// Description is the tool description from tool_schemas.json, except where publicDescriptions
	// replaces it with a text free of chat-loop instructions (the chat brain reads the schema text).
	Description string
	// Summary is a one-line, human-written description for indexes and lists (the
	// Description is written as a prompt for the model). TestEveryOfferedToolHasASummary
	// fails when an offered tool has none.
	Summary string
	// Parameters is the JSON Schema (type object) of the call arguments.
	Parameters map[string]any
	// ParamOrder lists the argument names in the order tool_schemas.json declares them
	// (Parameters is a map, which has no order).
	ParamOrder []string
	// Attribution names the upstreams behind the tool's answers.
	Attribution []tools.Source
	// Tier says who may run the tool on an internet-facing surface (see Tier). The
	// local user's own surfaces (the native loopback mount, the chat, cmd/mcp -all)
	// ignore it and run everything.
	Tier Tier
	// Upstream is true when a call can read a live upstream (ESI, or EvE-Scout: a cache miss
	// reaches it). Public surfaces give such tools one shared, stricter per-caller limit.
	Upstream bool
	// UpstreamName names the live upstream behind an Upstream tool: UpstreamESI or
	// UpstreamEveScout; empty for any other tool.
	UpstreamName string
	// UpstreamArgs lists the arguments that make a call of an Upstream tool live: a boolean that is
	// true, or a string that is not blank. A call with none of them set answers from the SDE and
	// spends no upstream budget. Empty = every call.
	UpstreamArgs []string
	// AcceptsJaniceKey is true for a tool that takes the caller's own Janice API key in
	// HeaderJaniceKey (REST and MCP over HTTP): with one it answers through Janice, without
	// one it still runs (appraise_items then estimates from ESI's average prices). The key is
	// per request, never logged or stored, and a public surface never substitutes its own.
	AcceptsJaniceKey bool
	// Data is the typed result struct that tools.Result.Data carries for this tool (the
	// "data" of the REST envelope, the structuredContent of an MCP result), or nil for a
	// tool that has only the text answer. A call can still end text-only: an unknown id
	// or a parse failure leaves Data null and says why in the text.
	Data reflect.Type
}

// SpendsUpstream reports whether a call with args reads a live upstream, and so counts against the
// per-caller live budget: the tool is an Upstream tool and either names no UpstreamArgs or one of
// them is set in args: a boolean parameter that is true, or a string parameter that is not blank.
func (t Tool) SpendsUpstream(args map[string]any) bool {
	if !t.Upstream {
		return false
	}
	if len(t.UpstreamArgs) == 0 {
		return true
	}
	props, _ := t.Parameters["properties"].(map[string]any)
	for _, a := range t.UpstreamArgs {
		prop, _ := props[a].(map[string]any)
		switch v := args[a].(type) {
		case bool:
			if v && prop["type"] == "boolean" {
				return true
			}
		case string:
			if prop["type"] == "string" && strings.TrimSpace(v) != "" {
				return true
			}
		}
	}
	return false
}

// LiveESI reports whether the tool's live upstream is ESI (the shared ESI error budget applies).
func (t Tool) LiveESI() bool { return t.UpstreamName == UpstreamESI }

// Tools returns every registered tool in tool_schemas.json order. The slice is the
// caller's; the values it points into are shared (see Tool).
func Tools() []Tool { return slices.Clone(registry()) }

// registry builds the tool list once: tool_schemas.json is embedded and immutable.
var registry = sync.OnceValue(func() []Tool {
	schemas := tools.ToolSchemas()
	raw := tools.ToolSchemasJSON()
	out := make([]Tool, 0, len(schemas))
	for _, entry := range schemas {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		if pub, ok := publicDescriptions()[name]; ok {
			desc = pub
		}
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, Tool{
			Name:             name,
			Description:      desc,
			Summary:          summaryTable()[name],
			Parameters:       params,
			ParamOrder:       paramOrder(raw, name),
			Attribution:      tools.Attribution(name),
			Tier:             tierOf(name),
			Upstream:         upstreamTools[name] != "",
			UpstreamName:     upstreamTools[name],
			UpstreamArgs:     slices.Clone(upstreamArgs()[name]),
			AcceptsJaniceKey: janiceKeyTools[name],
			Data:             dataType(name),
		})
	}
	return out
})

// publicDescriptions replaces the description of tools whose tool_schemas.json text steers the
// chat brain (a decision tree, what to tell or hide from the user). The registry feeds the public
// surfaces (REST, OpenAPI, llms.txt, MCP), which show the replacement; the chat keeps the schema text.
func publicDescriptions() map[string]string {
	return map[string]string{
		"get_fits": "Retrieve community ship fits from the indexed fit database, with author and source link for each. " +
			"Returns the raw EFT text (header + modules + drones + ammo) and the source URL for each match. " +
			"Filter by ship_name (exact), activity (pvp / pve / hauler / exploration / mining / logistics), " +
			"context (wh, abyss, abyss-electrical / firestorm / exotic / gamma / dark, abyss-t1..t6, ratting, solo), " +
			"cost_class (cheap < 50M / mid 50-500M / blingy > 500M ISK), alpha_clone (true for Alpha-skill-friendly), " +
			"combat_role (solo / fleet — zKillboard only), source (workbench / abysstracker / gustavmannfred / caldarijoans: restrict to one source), limit (default 5, at most 10), fit_id (fetch one fit by the id that list_fits and get_fits print; the other filters are ignored), " +
			"include_eft (false leaves the EFT blocks out and lists the ids only). If nothing matches, the answer says 'NO FITS FOUND'; " +
			"dropping the most restrictive filter may find some.",
	}
}

// Lookup returns the tool called name.
func Lookup(name string) (Tool, bool) {
	for _, t := range registry() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// ToolsIn returns the tools whose tier is one of tiers, in registry order.
func ToolsIn(tiers ...Tier) []Tool {
	return slices.DeleteFunc(Tools(), func(t Tool) bool { return !slices.Contains(tiers, t.Tier) })
}

// Tier is the access level of a tool on an internet-facing surface. The zero value
// is TierDisabled, so a tool nobody classified is refused rather than offered.
type Tier int

const (
	// TierDisabled tools are not offered on any public surface: not in the generated
	// documents, not in the MCP tool list, refused by the REST endpoint. They stay
	// available to the chat and to the local user's own surfaces.
	TierDisabled Tier = iota
	// TierPublic tools run for any caller, anonymous included: pure SDE lookups, the Gofa
	// fit calculations, the community-fit lookups (a payload-filtered corpus scroll) and the
	// ESI pass-throughs behind a stricter limit. The hosted service gives keyless callers
	// trial limits.
	TierPublic
	// TierKeyed tools need a valid API key: they lean on shared upstreams the project pays
	// for or must not hammer (the battle-report backends).
	TierKeyed
)

// HeaderJaniceKey is the request header that carries the caller's own Janice API key to a
// tool that accepts one (Tool.AcceptsJaniceKey) over REST and MCP-over-HTTP. It is per
// request, never logged and never stored.
const HeaderJaniceKey = "X-Janice-Key"

// JaniceKeyFrom returns the caller's Janice key from h, trimmed, or "" when none was sent.
// A value that is still an unexpanded config placeholder ("${...}", e.g. a Claude Code plugin
// header whose variable is unset) counts as no key, so the call answers with the ESI
// estimate instead of a failed upstream call with a bogus key.
func JaniceKeyFrom(h http.Header) string {
	v := strings.TrimSpace(h.Get(HeaderJaniceKey))
	if strings.HasPrefix(v, "${") {
		return ""
	}
	return v
}

// String returns the tier's name as the generated documents and error messages use it.
func (t Tier) String() string {
	switch t {
	case TierPublic:
		return "public"
	case TierKeyed:
		return "keyed"
	}
	return "disabled"
}

// Offered reports whether a public surface lists tools of this tier at all (everything
// but TierDisabled).
func (t Tier) Offered() bool { return t == TierPublic || t == TierKeyed }

// tierTable is the one place the policy lives. Flip a tool's tier here (convert_isk_to_real
// to TierPublic, say, or analyze_battle to TierDisabled) and every surface follows.
// TestEveryToolHasATier fails when a registry tool is missing from it.
func tierTable() map[string]Tier {
	return map[string]Tier{
		// SDE-only lookups and the Gofa fit calculations.
		"get_jumps_between":      TierPublic,
		"get_jump_distances":     TierPublic,
		"plan_jump_route":        TierPublic,
		"get_average_prices":     TierPublic,
		"get_market_history":     TierPublic,
		"get_systems_in_region":  TierPublic,
		"get_npc_stations":       TierPublic,
		"find_systems":           TierPublic,
		"get_system_info":        TierPublic,
		"get_thera_connections":  TierPublic,
		"get_reprocessing_yield": TierPublic,
		"get_required_skills":    TierPublic,
		"get_production_chain":   TierPublic,
		"find_canonical_module":  TierPublic,
		"search_item_by_name":    TierPublic,
		"get_ship_stats":         TierPublic,
		"get_hull_facts":         TierPublic,
		"get_ship_bonuses":       TierPublic,
		"compute_fit_stats":      TierPublic,
		"validate_fitting":       TierPublic,

		// Public ESI pass-throughs: public,
		// unauthenticated ESI routes served from the shared Expires cache, behind a stricter
		// per-IP limit (Tool.Upstream) and the upstream budget guard. Moving these four back
		// to TierKeyed is the rollback.
		"get_market_price":    TierPublic,
		"get_type_info":       TierPublic,
		"get_sovereignty":     TierPublic,
		"get_system_activity": TierPublic,

		// Community-fit lookups: they scroll the corpus by payload filters (no embedding call),
		// so they are public with trial limits; the free-text search path (q) is what needs a
		// key, and the HTTP layer enforces that. The battle-report pass-through stays keyed.
		"get_fits":       TierPublic,
		"list_fits":      TierPublic,
		"analyze_battle": TierKeyed,

		// Item appraisal: an estimate from ESI's average prices (one shared daily snapshot,
		// not a per-call ESI read, so not Upstream), or a Janice appraisal with the caller's
		// own key in X-Janice-Key (Tool.AcceptsJaniceKey).
		"appraise_items": TierPublic,

		// Real-money conversion: RMT optics under the DLA on a public surface. The chat
		// keeps it.
		"convert_isk_to_real": TierDisabled,
	}
}

// The live upstreams a tool can read (Tool.UpstreamName).
const (
	UpstreamESI      = "ESI"
	UpstreamEveScout = "EvE-Scout"
)

// upstreamTools are the tools whose cache misses reach a live upstream on a public surface
// (see Tool.Upstream), with the upstream's name. analyze_battle also calls ESI (and
// zKillboard, WarBeacon) but is keyed.
var upstreamTools = map[string]string{
	"get_market_price":      UpstreamESI,
	"get_type_info":         UpstreamESI,
	"get_sovereignty":       UpstreamESI,
	"get_system_activity":   UpstreamESI,
	"get_market_history":    UpstreamESI,
	"get_thera_connections": UpstreamEveScout,
	"plan_jump_route":       UpstreamESI,
	// get_production_chain reads ESI only with system (the cost index); include_cost reads the
	// shared daily price copy.
	"get_production_chain": UpstreamESI,
}

// upstreamArgs are the arguments (a true boolean or a non-blank string) that make a call of a tool live (Tool.UpstreamArgs); a
// tool in upstreamTools without an entry is live on every call.
func upstreamArgs() map[string][]string {
	return map[string][]string{
		"plan_jump_route":      {"avoid_incursions", "live_info"},
		"get_production_chain": {"system"},
	}
}

// janiceKeyTools are the tools that take the caller's own Janice key (Tool.AcceptsJaniceKey).
var janiceKeyTools = map[string]bool{"appraise_items": true}

// summaryTable holds the one-line human summary of each tool (Tool.Summary). Keep each
// to one sentence without a trailing period issue: it is shown in lists and the site search.
func summaryTable() map[string]string {
	return map[string]string{
		"get_market_price":       "Best sell and buy price and order counts for an item in The Forge or at a main trade hub.",
		"get_type_info":          "Name, description, volume, mass and group of an item.",
		"search_item_by_name":    "Resolve item names to type IDs; with details, group, meta level, market group, volume and Alpha use of up to 50 items.",
		"get_ship_stats":         "Slot layout, hardpoints and resource limits of a ship.",
		"get_ship_bonuses":       "Hull bonuses of a ship: per skill level, role bonuses and ship class.",
		"find_canonical_module":  "Canonical module names of a family, or a module search by slot, kind and meta group.",
		"get_hull_facts":         "A hull's fitting envelope: slots, hardpoints, drone bay, bonuses and weapon restrictions.",
		"validate_fitting":       "Check an EFT fit against its slots, CPU and powergrid.",
		"compute_fit_stats":      "DPS, EHP, speed, align time and capacitor of an EFT fit, from the Gofa engine.",
		"get_production_chain":   "Material breakdown to build an item down to base materials; with system or include_cost, the ISK cost.",
		"get_jumps_between":      "Jump count and route between two solar systems.",
		"get_jump_distances":     "Jump counts from one solar system to up to 200 others in one call.",
		"plan_jump_route":        "Jump-drive routes for any jump-capable hull or an Ansiblex chain: hops, light-years, gate jumps and fuel.",
		"get_market_history":     "Price and volume history of an item in a region, by day or week (up to 90 days).",
		"get_average_prices":     "Daily average prices of up to 200 items in one call (ESI /markets/prices).",
		"get_systems_in_region":  "Solar systems of a region, filtered by security band.",
		"get_npc_stations":       "NPC stations in a system or near one, by services and mission agents, with owner and refinery.",
		"find_systems":           "Search solar systems by security, location, celestials, stations, ice belts and distance.",
		"get_system_info":        "Security, location, neighbours, celestials, stations and ice belt of up to 50 systems.",
		"get_thera_connections":  "Live Thera and Turnur wormhole connections from EvE-Scout, optionally nearest to a system.",
		"get_reprocessing_yield": "Materials produced by reprocessing an item.",
		"get_required_skills":    "Skills to fly a ship or build an item, for up to 20 items at once, with SP and training time.",
		"get_sovereignty":        "Alliance, corporation or faction holding sovereignty over up to 50 solar systems.",
		"get_system_activity":    "Jumps and ship, pod and NPC kills in up to 50 solar systems in the last hour.",
		"appraise_items":         "Total ISK value of an item list: an estimate from ESI average prices, or a Janice appraisal with your own Janice key.",
		"get_fits":               "Community fits for a ship, by activity, cost and source, with author credit.",
		"list_fits":              "List community fits by exact filters: source, ship, tag or filament.",
		"analyze_battle":         "Summarise a battle report from zKillboard, WarBeacon or br.evetools.org.",
		"convert_isk_to_real":    "Convert an ISK amount to a real-money value (not offered on public surfaces).",
	}
}

// paramOrder returns the argument names of the tool name in the order the raw
// tool_schemas.json declares them. A malformed document yields nil; the caller falls back
// to sorting.
func paramOrder(raw []byte, name string) []string {
	var doc []struct {
		Function struct {
			Name       string `json:"name"`
			Parameters struct {
				Properties orderedKeys `json:"properties"`
			} `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	for _, e := range doc {
		if e.Function.Name == name {
			return e.Function.Parameters.Properties
		}
	}
	return nil
}

// orderedKeys decodes a JSON object into its keys, in document order.
type orderedKeys []string

func (o *orderedKeys) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // the opening brace
		return err
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := k.(string)
		*o = append(*o, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return nil
}

// tierOf returns a tool's tier; a tool missing from the table is disabled.
func tierOf(name string) Tier { return tierTable()[name] }

// dataType maps a tool to its typed result (see Tool.Data). TestDataTypesMatchDispatch
// fails when tools.dispatch hands out a typed result for a tool missing here.
func dataType(name string) reflect.Type {
	switch name {
	case "get_type_info":
		return reflect.TypeFor[tools.TypeInfo]()
	case "search_item_by_name":
		return reflect.TypeFor[tools.ItemList]()
	case "get_market_price":
		return reflect.TypeFor[tools.MarketPrice]()
	case "get_average_prices":
		return reflect.TypeFor[tools.AveragePrices]()
	case "get_production_chain":
		return reflect.TypeFor[tools.ProductionChain]()
	case "get_market_history":
		return reflect.TypeFor[tools.MarketHistory]()
	case "get_jumps_between":
		return reflect.TypeFor[tools.Route]()
	case "get_jump_distances":
		return reflect.TypeFor[tools.JumpDistances]()
	case "plan_jump_route":
		return reflect.TypeFor[tools.JumpRoute]()
	case "find_systems":
		return reflect.TypeFor[tools.SystemList]()
	case "get_thera_connections":
		return reflect.TypeFor[tools.TheraConnections]()
	case "get_system_info":
		return reflect.TypeFor[tools.SystemInfoList]()
	case "get_sovereignty":
		return reflect.TypeFor[tools.SovereigntyList]()
	case "get_system_activity":
		return reflect.TypeFor[tools.ActivityList]()
	case "get_ship_stats":
		return reflect.TypeFor[tools.ShipStats]()
	case "validate_fitting":
		return reflect.TypeFor[tools.FitValidation]()
	case "compute_fit_stats":
		return reflect.TypeFor[tools.FitStatCard]()
	case "get_required_skills":
		return reflect.TypeFor[tools.SkillPlan]()
	case "get_npc_stations":
		return reflect.TypeFor[tools.StationList]()
	case "find_canonical_module":
		return reflect.TypeFor[tools.ModuleList]()
	case "get_fits":
		return reflect.TypeFor[tools.CommunityFits]()
	case "list_fits":
		return reflect.TypeFor[tools.FitListing]()
	}
	return nil
}
