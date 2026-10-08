package dataapi

import (
	"net/http"
	"reflect"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

// Surface says which optional parts a service exposes. The generated documents
// (OpenAPI, llms.txt) describe exactly the Surface they are given, and New registers
// exactly the routes the Surface of its Config names, from the one route table below.
type Surface struct {
	// Prefix is the route prefix ("" means DefaultPrefix).
	Prefix string
	// ToolAPI is the raw tool endpoint mode.
	ToolAPI ToolAPIMode
	// MCP is true when the MCP streamable-HTTP endpoint is mounted at {prefix}/mcp.
	MCP bool
	// Auth is true when the service accepts API keys, which makes the keyed tier
	// available and documented.
	Auth bool
	// Docs is true when the description routes (openapi.yaml, openapi.json, /llms.txt,
	// /llms-full.txt) are served.
	Docs bool
	// Limits are the per-route budgets stated in the documents.
	Limits Limits
}

// PublicSurface is what the internet-facing cmd/dataapi serves with every optional part
// switched on, at the default prefix and default limits. The committed generated files
// (api/openapi.yaml, llms.txt, llms-full.txt) describe it.
func PublicSurface() Surface {
	return Surface{Prefix: DefaultPrefix, ToolAPI: ToolAPIPublic, MCP: true, Docs: true, Auth: true, Limits: DefaultLimits()}
}

func (s Surface) prefix() string {
	if s.Prefix == "" {
		return DefaultPrefix
	}
	return s.Prefix
}

// route is one registered endpoint and, for the prefixed ones, its documentation.
type route struct {
	method string
	// path is relative to the prefix, or absolute when root is set.
	path string
	root bool
	// anyMethod hands every method to the handler (the MCP transport answers its own
	// 405s); method is then only the one the OpenAPI document describes.
	anyMethod bool
	// when reports whether the route exists on a Surface; nil means always.
	when   func(Surface) bool
	rate   func(Surface) Rate
	handle func(*API, http.ResponseWriter, *http.Request)
	// doc is nil for a route the OpenAPI document leaves out (the root-level llms files).
	doc *opDoc
}

// opDoc documents one operation.
type opDoc struct {
	id, tag, summary, description string
	query                         []queryParam
	body                          *bodyDoc
	ok                            okDoc
	// errors are the non-2xx statuses the operation can answer (405 and the request-ID
	// header are common to every route and stated once).
	errors []int
	// perTool marks the raw tool endpoint: the document expands it into one operation
	// per exposed tool, with that tool's argument schema and result type.
	perTool bool
}

type queryParam struct {
	name        string
	description string
	required    bool
	schema      schema
}

// bodyDoc documents a JSON request body: its Go type, which properties are required and
// per-property overrides (description, enum, ...). Raw replaces the reflected schema.
type bodyDoc struct {
	name      string // component name
	typ       reflect.Type
	required  []string
	overrides map[string]schema
	raw       schema
}

// okDoc documents the 200 response: a Go type (reflected), or a raw schema with a media
// type for non-JSON bodies.
type okDoc struct {
	description string
	typ         reflect.Type
	media       string
	raw         schema
}

func always(Surface) bool { return true }

func toolAPIOn(s Surface) bool { return s.ToolAPI != ToolAPIOff }
func mcpOn(s Surface) bool     { return s.MCP }
func docsOn(s Surface) bool    { return s.Docs }

func unlimited(Surface) Rate { return Rate{} }

// errorCodes are the stable `error.code` values of the JSON error shape.
func errorCodes() []string {
	return []string{
		"not_found", "method_not_allowed", "invalid_request", "payload_too_large", "rate_limited",
		"unavailable", "upstream_error", "unknown_tool", "tool_disabled", "api_key_required", "invalid_api_key",
		"key_not_permitted", "janice_key_required", "timeout", "internal_error",
	}
}

// routeTable is the single list of the API's endpoints: New registers from it and the
// OpenAPI document is generated from it, so neither can drift from the other. It is a
// function so nothing can mutate it at runtime.
func routeTable() []route {
	return []route{
		{
			method: http.MethodGet, path: "/fits/search", when: always,
			rate:   func(s Surface) Rate { return s.Limits.FitsSearch },
			handle: (*API).fitsSearch,
			doc: &opDoc{
				id: "searchFits", tag: "fits", summary: "Search community fits",
				description: "Searches the community-fit corpus (EVE Workbench, Abysstracker and other sources named in each hit). " +
					"The facet parameters filter the corpus; with `q` a filtered vector search ranks the hits. " +
					"When the facets match nothing they are relaxed one at a time, last first: `relaxed` is then true and `dropped` names " +
					"what was removed. `ship` may be a loose hull name (`megath` finds Megathron). No language model is involved. " +
					"Every hit carries an `attribution` object (`source`, `source_url`, `author` when known, `license`, `credit` and `license_url` when the terms require them); " +
					"show it wherever you show the fit. The fits are community data served ranked and capped, not for bulk export.",
				query: []queryParam{
					{name: "ship", description: "Hull name; a partial or loose name is resolved against the SDE."},
					{name: "activity", description: "Activity tag, e.g. `pve` or `pvp`."},
					{name: "tag", description: "Extra tag, e.g. `abyss` or `ratting`."},
					{name: "filament", description: "Abyssal filament type."},
					{name: "cost", description: "Cost class tag, e.g. `cheap`."},
					{name: "source", description: "Restrict to one corpus source; empty searches the default source set."},
					{name: "clone", description: "`alpha` keeps only alpha-clone fits, `omega` only omega-only fits; empty keeps both.", schema: schema{"enum": []any{"alpha", "omega"}}},
					{name: "q", description: "Free-text query; empty lists by the facets alone."},
					{name: "limit", description: "Maximum hits, clamped to 1-24 (the response `limit` and `note` say so). There is no offset, page or cursor: one query reaches one ranked page, and a request carrying such a parameter is refused with `invalid_request`.", schema: schema{"type": "integer", "default": 24}},
				},
				ok:     okDoc{description: "The ranked fits.", typ: reflect.TypeFor[rag.FitSearchResult]()},
				errors: []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodPost, path: "/fits/detail", when: always,
			rate:   func(s Surface) Rate { return s.Limits.FitsDetail },
			handle: (*API).fitsDetail,
			doc: &opDoc{
				id: "fitDetail", tag: "fits", summary: "Describe a fit",
				description: "Parses an EFT block and returns its slots, CPU / powergrid / calibration use and ship class. " +
					"Deterministic; DPS and EHP are not part of this view (see `/fit/stats`).",
				body: &bodyDoc{
					name: "FitDetailRequest", typ: reflect.TypeFor[fitDetailRequest](), required: []string{"eft"},
					overrides: map[string]schema{
						"eft":  {"description": "The fit as an EFT block (`[Hull, Name]` header, then one module per line)."},
						"cost": {"description": "Also price the fit when a price source is configured on the server."},
					},
				},
				ok:     okDoc{description: "The fit breakdown.", typ: reflect.TypeFor[tools.FitDetail]()},
				errors: []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodPost, path: "/fit/stats", when: always,
			rate:   func(s Surface) Rate { return s.Limits.FitStats },
			handle: (*API).fitStats,
			doc: &opDoc{
				id: "fitStats", tag: "fits", summary: "Compute fit statistics",
				description: "Parses an EFT block and returns the Gofa engine's DPS, tank, capacitor, navigation, targeting and drone " +
					"numbers. Deterministic; the same engine produces the numbers of the `compute_fit_stats` tool.",
				body: &bodyDoc{
					name: "FitStatsRequest", typ: reflect.TypeFor[fitStatsRequest](), required: []string{"eft"},
					overrides: map[string]schema{
						"eft":          {"description": "The fit as an EFT block."},
						"activeDrones": {"description": "Type IDs of the drones to launch, in priority order; omitted means automatic."},
					},
				},
				ok:     okDoc{description: "The computed statistics.", typ: reflect.TypeFor[fit.FitStats]()},
				errors: []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodPost, path: "/fit/suggest", when: always,
			rate:   func(s Surface) Rate { return s.Limits.FitSuggest },
			handle: (*API).fitSuggest,
			doc: &opDoc{
				id: "fitSuggest", tag: "fits", summary: "Suggest modules for an empty slot",
				description: "Ranks the modules that can fill a slot tier of the given fit: how often similar community fits carry them " +
					"(when the corpus is up), their meta tier, and whether they fit the remaining CPU and powergrid. " +
					"Without the corpus the ranking falls back to the SDE alone.",
				body: &bodyDoc{
					name: "FitSuggestRequest", typ: reflect.TypeFor[fitSuggestRequest](), required: []string{"eft", "slot"},
					overrides: map[string]schema{
						"eft":  {"description": "The current fit as an EFT block."},
						"slot": {"description": "The slot tier to fill.", "enum": []any{"high", "mid", "low", "rig"}},
					},
				},
				ok:     okDoc{description: "At most 12 ranked suggestions.", typ: reflect.TypeFor[[]fit.Suggestion]()},
				errors: []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodGet, path: "/items/search", when: always,
			rate:   func(s Surface) Rate { return s.Limits.ItemsSearch },
			handle: (*API).itemsSearch,
			doc: &opDoc{
				id: "searchItems", tag: "items", summary: "Search modules, charges and drones",
				description: "Name search over the SDE's fittable modules, charges or drones (the fitting palette's search).",
				query: []queryParam{
					{name: "q", description: "Name fragment; at least 2 characters.", required: true, schema: schema{"type": "string", "minLength": 2}},
					{name: "kind", description: "What to search.", schema: schema{"enum": []any{"module", "charge", "drone"}, "default": "module"}},
					{name: "slot", description: "For modules: restrict to one slot tier.", schema: schema{"enum": []any{"high", "mid", "low", "rig"}}},
					{name: "limit", description: "Maximum hits.", schema: schema{"type": "integer", "default": 30}},
				},
				ok:     okDoc{description: "The matching items.", typ: reflect.TypeFor[[]sde.ModuleHit]()},
				errors: []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodPost, path: "/tool/{name}", when: toolAPIOn,
			rate: func(s Surface) Rate {
				if s.ToolAPI == ToolAPILoopback {
					return Rate{} // the user's own loopback: no limit
				}
				return s.Limits.Tool
			},
			handle: (*API).tool,
			doc: &opDoc{
				id: "tool", tag: "tools", summary: "Run a tool",
				description: "Runs one deterministic tool in-process. The body is the tool's arguments object; the answer is the envelope " +
					"`{tool, version, result: {text, data}, attribution}`. `result.text` is the text the chat assistant reads, `result.data` " +
					"the typed result for the tools that have one (null otherwise), `attribution` the upstreams behind the answer.",
				perTool: true,
			},
		},
		{
			method: http.MethodPost, path: "/mcp", anyMethod: true, when: mcpOn,
			rate:   func(s Surface) Rate { return s.Limits.MCP },
			handle: (*API).mcpTransport,
			doc: &opDoc{
				id: "mcp", tag: "mcp", summary: "Model Context Protocol endpoint",
				description: "Streamable-HTTP MCP server (stateless, JSON responses): the same tools as `/tool/{name}`, for MCP clients such as " +
					"Claude Desktop or Claude Code. POST one JSON-RPC 2.0 message per request (`initialize`, `tools/list`, `tools/call`, ...); " +
					"a notification is answered `202` with no body. `GET` and `DELETE` are not supported in stateless mode (405). " +
					"Point a client at this URL; it is not meant to be called by hand.",
				body: &bodyDoc{raw: schema{
					"type":        "object",
					"description": "A JSON-RPC 2.0 request or notification (see https://modelcontextprotocol.io/specification).",
					"required":    []any{"jsonrpc", "method"},
					"properties": propList{
						{name: "jsonrpc", schema: schema{"const": "2.0"}},
						{name: "id", schema: schema{"type": []any{"string", "integer"}}},
						{name: "method", schema: schema{"type": "string", "examples": []any{"tools/list"}}},
						{name: "params", schema: schema{"type": "object"}},
					},
				}},
				ok: okDoc{description: "The JSON-RPC 2.0 response.", raw: schema{
					"type":        "object",
					"description": "A JSON-RPC 2.0 response: `result` on success, `error` otherwise.",
					"required":    []any{"jsonrpc"},
					"properties": propList{
						{name: "jsonrpc", schema: schema{"const": "2.0"}},
						{name: "id", schema: schema{"type": []any{"string", "integer", "null"}}},
						{name: "result", schema: schema{"type": "object"}},
						{name: "error", schema: schema{"type": "object"}},
					},
				}},
				errors: []int{http.StatusRequestEntityTooLarge, http.StatusTooManyRequests},
			},
		},
		{
			method: http.MethodGet, path: "/health", when: always, rate: unlimited,
			handle: (*API).health,
			doc: &opDoc{
				id: "health", tag: "meta", summary: "Liveness probe",
				description: "Always 200 while the process serves; a degraded dependency shows in the body, never in the status. The flags say which " +
					"dependencies loaded, so a degraded service (no SDE, no fit corpus) can be told from a healthy one. `corpus` is true " +
					"only when the fit corpus is configured and its backend answered a probe (cached for 30 seconds, 1 second timeout); " +
					"`corpus_configured` is true when a corpus is wired at all, reachable or not.",
				ok: okDoc{description: "The service is up.", typ: reflect.TypeFor[healthResponse]()},
			},
		},
		{
			method: http.MethodGet, path: "/openapi.yaml", when: docsOn,
			rate:   func(s Surface) Rate { return s.Limits.Docs },
			handle: (*API).serveOpenAPIYAML,
			doc: &opDoc{
				id: "openapiYaml", tag: "meta", summary: "This API description (YAML)",
				description: "The OpenAPI 3.1 document of this service, generated from its route table and tool registry.",
				ok:          okDoc{description: "The OpenAPI document.", media: "application/yaml", raw: schema{"type": "string"}},
				errors:      []int{http.StatusTooManyRequests},
			},
		},
		{
			method: http.MethodGet, path: "/openapi.json", when: docsOn,
			rate:   func(s Surface) Rate { return s.Limits.Docs },
			handle: (*API).serveOpenAPIJSON,
			doc: &opDoc{
				id: "openapiJson", tag: "meta", summary: "This API description (JSON)",
				description: "The same OpenAPI 3.1 document as JSON.",
				ok:          okDoc{description: "The OpenAPI document.", media: "application/json", raw: schema{"type": "object"}},
				errors:      []int{http.StatusTooManyRequests},
			},
		},
		{
			method: http.MethodGet, path: "/llms.txt", root: true, when: docsOn,
			rate:   func(s Surface) Rate { return s.Limits.Docs },
			handle: (*API).serveLLMs,
		},
		{
			method: http.MethodGet, path: "/llms-full.txt", root: true, when: docsOn,
			rate:   func(s Surface) Rate { return s.Limits.Docs },
			handle: (*API).serveLLMsFull,
		},
	}
}
