// Package catalog is the one tool registry the generated public surfaces read: the
// OpenAPI document, llms.txt / llms-full.txt, the REST tool endpoint and the MCP server
// (R3.3). It joins what core/tools already owns, split across three places, into one
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
// imports only core/tools, so a surface can depend on it without a path to anything else.
package catalog

import (
	"reflect"
	"slices"
	"sync"

	"eve-cyno.dev/go/data/tools"
)

// Tool is one entry of the registry. Parameters is shared with tools.ToolSchemas: callers
// must treat it (and everything below it) as read-only.
type Tool struct {
	// Name is the tool name as called (also the last segment of POST /v1/tool/{name}).
	Name string
	// Description is the tool description from tool_schemas.json.
	Description string
	// Parameters is the JSON Schema (type object) of the call arguments.
	Parameters map[string]any
	// Attribution names the upstreams behind the tool's answers.
	Attribution []tools.Source
	// Tier says who may run the tool on an internet-facing surface (see Tier). The
	// local user's own surfaces (the native loopback mount, the chat, cmd/mcp -all)
	// ignore it and run everything.
	Tier Tier
	// Data is the typed result struct that tools.Result.Data carries for this tool (the
	// "data" of the REST envelope, the structuredContent of an MCP result), or nil for a
	// tool that has only the text answer. A call can still end text-only: an unknown id
	// or a parse failure leaves Data null and says why in the text.
	Data reflect.Type
}

// Tools returns every registered tool in tool_schemas.json order. The slice is the
// caller's; the values it points into are shared (see Tool).
func Tools() []Tool { return slices.Clone(registry()) }

// registry builds the tool list once: tool_schemas.json is embedded and immutable.
var registry = sync.OnceValue(func() []Tool {
	schemas := tools.ToolSchemas()
	out := make([]Tool, 0, len(schemas))
	for _, entry := range schemas {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, Tool{
			Name:        name,
			Description: desc,
			Parameters:  params,
			Attribution: tools.Attribution(name),
			Tier:        tierOf(name),
			Data:        dataType(name),
		})
	}
	return out
})

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

// Tier is the access level of a tool on an internet-facing surface (R3.4). The zero value
// is TierDisabled, so a tool nobody classified is refused rather than offered.
type Tier int

const (
	// TierDisabled tools are not offered on any public surface: not in the generated
	// documents, not in the MCP tool list, refused by the REST endpoint. They stay
	// available to the chat and to the local user's own surfaces.
	TierDisabled Tier = iota
	// TierPublic tools run for any caller, anonymous included: pure SDE lookups and the
	// Gofa fit calculations (no per-call cost to the project).
	TierPublic
	// TierKeyed tools need a valid API key: they cost the project something per call
	// (corpus search embeds the query through a paid provider) or lean on a shared
	// upstream (ESI, the battle-report backends).
	TierKeyed
	// TierBYOKey tools run only with the caller's own credential for the upstream: the
	// project's key is never used for them on a public surface. appraise_items takes the
	// caller's Janice key in the X-Janice-Key header.
	TierBYOKey
)

// HeaderJaniceKey is the request header that carries the caller's own Janice API key to
// a TierBYOKey tool over REST and MCP-over-HTTP. It is per request, never logged and
// never stored.
const HeaderJaniceKey = "X-Janice-Key"

// String returns the tier's name as the generated documents and error messages use it.
func (t Tier) String() string {
	switch t {
	case TierPublic:
		return "public"
	case TierKeyed:
		return "keyed"
	case TierBYOKey:
		return "byo-key"
	}
	return "disabled"
}

// Offered reports whether a public surface lists tools of this tier at all (everything
// but TierDisabled).
func (t Tier) Offered() bool { return t == TierPublic || t == TierKeyed || t == TierBYOKey }

// tierTable is the one place the policy lives. Flip a tool's tier here (convert_isk_to_real
// to TierPublic, say, or analyze_battle to TierDisabled) and every surface follows.
// TestEveryToolHasATier fails when a registry tool is missing from it.
func tierTable() map[string]Tier {
	return map[string]Tier{
		// SDE-only lookups and the Gofa fit calculations.
		"get_jumps_between":      TierPublic,
		"get_systems_in_region":  TierPublic,
		"get_npc_stations":       TierPublic,
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

		// Corpus search (embedding cost) and the ESI / battle-report pass-throughs.
		"get_fits":            TierKeyed,
		"list_fits":           TierKeyed,
		"get_market_price":    TierKeyed,
		"get_type_info":       TierKeyed,
		"get_sovereignty":     TierKeyed,
		"get_system_activity": TierKeyed,
		"analyze_battle":      TierKeyed,

		// The caller's own Janice key.
		"appraise_items": TierBYOKey,

		// Real-money conversion: RMT optics under the DLA on a public surface. The chat
		// keeps it.
		"convert_isk_to_real": TierDisabled,
	}
}

// tierOf returns a tool's tier; a tool missing from the table is disabled.
func tierOf(name string) Tier { return tierTable()[name] }

// dataType maps a tool to its typed result (see Tool.Data). TestDataTypesMatchDispatch
// fails when tools.dispatch hands out a typed result for a tool missing here.
func dataType(name string) reflect.Type {
	switch name {
	case "get_type_info":
		return reflect.TypeFor[tools.TypeInfo]()
	case "get_market_price":
		return reflect.TypeFor[tools.MarketPrice]()
	case "get_ship_stats":
		return reflect.TypeFor[tools.ShipStats]()
	case "validate_fitting":
		return reflect.TypeFor[tools.FitValidation]()
	case "compute_fit_stats":
		return reflect.TypeFor[tools.FitStatCard]()
	case "get_fits":
		return reflect.TypeFor[tools.CommunityFits]()
	case "list_fits":
		return reflect.TypeFor[tools.FitListing]()
	}
	return nil
}
