package dataapi

import (
	"net/http"
	"net/netip"
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
	// SelfServe is true when this service runs the key store behind self-serve issuance
	// (Discord /apikey and the ai.eve-cyno.dev settings): only then do the pages show the
	// hosted "how to get a key" copy. Auth alone (a key file) says keys are issued by the
	// operator.
	SelfServe bool
	// KeyedLimitFactor is the multiple of the route limits an API key gets on a route with no
	// explicit Rate.KeyedMax; it applies when Auth is on and it is at least 2.
	KeyedLimitFactor int
	// KeyDailyCap is the default daily request cap of a new API key (a key's own cap can
	// differ); the documents state it (and the daily_quota_exceeded refusal) when Auth is
	// on and it is above 0; 0 leaves both out.
	KeyDailyCap int
	// AnonDailyCap is the number of requests per UTC day a client without an API key gets;
	// the documents state it when Auth is on and it is above 0.
	AnonDailyCap int
	// Docs is true when the description routes (openapi.yaml, openapi.json, /llms.txt,
	// /llms-full.txt) are served.
	Docs bool
	// Limits are the per-route budgets stated in the documents.
	Limits Limits
	// Ranges are the networks of Anthropic's connector calls (Config.AnthropicRanges). The
	// documents state the connector budgets (Limits.Range) only where this is set on a service
	// that accepts keys, so they never describe a tier the server does not apply.
	Ranges []netip.Prefix
	// PublicURL, when set (validated, no trailing slash), makes the OpenAPI servers entry
	// and the llms.txt links absolute. PublicSurface leaves it empty so the committed
	// generated files stay host-independent.
	PublicURL string
}

// DefaultKeyedLimitFactor is the multiple of the route limits an API key gets on the public
// service (used by PublicSurface, the documents tests and cmd/dataapi).
const DefaultKeyedLimitFactor = 2

// DefaultKeyDailyCap is the daily request cap a new API key gets on the public service.
const DefaultKeyDailyCap = 10000

// DefaultAnthropicRanges are Anthropic's outbound IPv4 ranges, the addresses its MCP tool calls to
// external servers (claude.ai and Claude Desktop custom connectors) come from; Anthropic
// publishes no outbound IPv6. Source: https://platform.claude.com/docs/en/api/ip-addresses
// (outbound requests). The compose file passes the same default.
const DefaultAnthropicRanges = "160.79.104.0/21,153.61.208.0/20"

// anthropicIPsURL is the page that publishes DefaultAnthropicRanges; the documents link it.
const anthropicIPsURL = "https://platform.claude.com/docs/en/api/ip-addresses"

// rangeTierDocs reports whether the documents state the connector range tier: it exists only
// on a service that accepts keys and has ranges configured.
func (s Surface) rangeTierDocs() bool { return s.Auth && len(s.Ranges) > 0 }

// rangeDailyCap is the daily cap of a keyless address in the ranges: the larger of
// Limits.Range.DailyCap and AnonDailyCap (see API.anonCapOf).
func (s Surface) rangeDailyCap() int { return max(s.Limits.Range.DailyCap, s.AnonDailyCap) }

// PublicSurface is what the internet-facing cmd/dataapi serves with every optional part
// switched on, at the default prefix and default limits. The committed generated files
// (api/openapi.yaml, llms.txt, llms-full.txt) describe it.
func PublicSurface() Surface {
	ranges, err := ParseRanges(DefaultAnthropicRanges)
	if err != nil {
		panic("dataapi: DefaultAnthropicRanges: " + err.Error()) // a constant: unreachable
	}
	return Surface{Ranges: ranges, Prefix: DefaultPrefix, ToolAPI: ToolAPIPublic, MCP: true, Docs: true, Auth: true, SelfServe: true, KeyedLimitFactor: DefaultKeyedLimitFactor, KeyDailyCap: DefaultKeyDailyCap, AnonDailyCap: DefaultAnonDailyCap, Limits: DefaultLimits()}
}

// keyedRate is the plain budget a caller with an API key gets on route budget r. A service
// without keys has no such caller: the budget is the plain one.
func (s Surface) keyedRate(r Rate) Rate {
	if !s.Auth {
		return r.forAnon()
	}
	return r.forKey(s.KeyedLimitFactor)
}

// keysRaise reports whether an API key raises the limit of any route on this Surface.
func (s Surface) keysRaise() bool {
	l := s.Limits
	for _, r := range []Rate{l.FitsSearch, l.FitsDetail, l.FitStats, l.FitSuggest, l.ItemsSearch, l.Tool, l.MCP, l.UpstreamTool, l.Docs} {
		if r.Max > 0 && s.keyedRate(r).Max > r.Max {
			return true
		}
	}
	return false
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
	when func(Surface) bool
	rate func(Surface) Rate
	// rangeRate, when set, gives the route a range tier: the per-address rate and the shared
	// ceiling of keyless callers inside Config.AnthropicRanges (see rangeLimiter).
	rangeRate func(Surface) (perAddr, ceiling Rate)
	handle    func(*API, http.ResponseWriter, *http.Request)
	// trialFree marks the routes the anonymous daily cap does not count: browsing the
	// documentation (and the portal assets) never spends the trial, and a health probe must
	// not be locked out.
	trialFree bool
	// cache is the Cache-Control policy of a 200 answer ("" leaves the header unset);
	// every other status is no-store.
	cache string
	// doc is nil for a route the OpenAPI document leaves out (the root-level llms files).
	doc *opDoc
}

// opDoc documents one operation.
type opDoc struct {
	id, tag, summary, description string
	// pathParams document the {name} segments of the path (always required).
	pathParams []queryParam
	query      []queryParam
	body       *bodyDoc
	ok         okDoc
	// notModified, when set, documents a 304 answer to a matching If-None-Match: no body,
	// the 200's headers.
	notModified string
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
// type for non-JSON bodies. Headers are the response headers it documents besides
// X-Request-ID, each an OpenAPI header object.
type okDoc struct {
	description string
	typ         reflect.Type
	media       string
	raw         schema
	headers     map[string]schema
}

func always(Surface) bool { return true }

func toolAPIOn(s Surface) bool { return s.ToolAPI != ToolAPIOff }
func mcpOn(s Surface) bool     { return s.MCP }
func docsOn(s Surface) bool    { return s.Docs }

func unlimited(Surface) Rate { return Rate{} }

// stringsToAny converts a string list to the []any of a schema enum.
func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// errorCodes are the stable `error.code` values of the JSON error shape.
func errorCodes() []string {
	return []string{
		"not_found", "method_not_allowed", "invalid_request", "payload_too_large", "rate_limited",
		"daily_quota_exceeded", "unavailable", "upstream_error", "upstream_busy", "busy", "unknown_tool", "tool_disabled", "api_key_required", "invalid_api_key",
		"key_not_permitted", "invalid_arguments", "timeout", "internal_error",
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
				id: searchFitsID, tag: "fits", summary: "Search community fits",
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
			cache:  cacheNone,
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
			cache:  cacheNone,
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
			cache:  cacheNone,
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
			cache:  cacheSDE,
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
			cache:  cacheNone,
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
			rate:      func(s Surface) Rate { return s.Limits.MCP },
			rangeRate: func(s Surface) (Rate, Rate) { return s.Limits.Range.MCP, s.Limits.Range.MCPCeiling },
			handle:    (*API).mcpTransport,
			cache:     cacheNone,
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
				errors: []int{http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable},
			},
		},
		{
			method: http.MethodGet, path: "/health", when: always, rate: unlimited, trialFree: true,
			handle: (*API).health,
			cache:  cacheNone,
			doc: &opDoc{
				id: "health", tag: "meta", summary: "Liveness probe",
				description: "Always 200 while the process serves; a degraded dependency shows in the body, never in the status. The flags say which " +
					"dependencies loaded, so a degraded service (no SDE, no fit corpus) can be told from a healthy one. `corpus` is true " +
					"only when the fit corpus is configured and its backend answered a probe (cached for 30 seconds, 1 second timeout); " +
					"`corpus_configured` is true when a corpus is wired at all, reachable or not. `sde_build` is the build id of the SDE " +
					"the service reads, when known.",
				ok: okDoc{description: "The service is up.", typ: reflect.TypeFor[healthResponse]()},
			},
		},
		{
			// Public files rewritten on a fixed cadence and served from memory: like the portal
			// assets, no per-address limit, no anonymous daily cap and no in-flight slot. The
			// handler sets each file's Cache-Control; anything it leaves unset is no-store.
			method: http.MethodGet, path: "/datasets/{file}", when: always, rate: unlimited, trialFree: true,
			handle: (*API).dataset,
			cache:  cacheNone,
			doc: &opDoc{
				id: "getDataset", tag: "datasets", summary: "Download a published intel dataset",
				description: "Serves one file the intel collector publishes, byte for byte: `intel-gates-now.json` (killmails on stargates over the " +
					"last two hours, rewritten every minute; `Cache-Control: public, max-age=60`), `intel-systems-hour.json` (the latest ESI hour of " +
					"jumps and kills per system; `max-age=300`) and `intel-gates-profile.json` (the 28-day profile of kills per gate and UTC hour, " +
					"rebuilt daily; `max-age=3600`). Each file names its own `format` and major `version`; a reader accepts the majors it knows and " +
					"ignores fields it does not know. The killmail-based files credit zKillboard; all of it is Fenris Creations game data (ESI), " +
					"and each file carries its `credit` line. " +
					"The answer carries a strong `ETag` (the first 32 hex digits of the body's SHA-256); `If-None-Match` with that tag, weak or " +
					"strong, answers `304` with no body. `Accept-Encoding: gzip` gets the same bytes gzip-compressed, with the same `ETag`. " +
					"The route has no rate limit and is never counted against the anonymous daily request cap; " +
					"a request with a query string may be refused with `400` (send none), so poll at the file's `max-age` and revalidate " +
					"with `If-None-Match`. " +
					"`404` means the file is not published yet, or this service serves no datasets. Errors are never cached.",
				pathParams: []queryParam{
					{name: "file", description: "The dataset file name.", schema: schema{"enum": stringsToAny(datasetNames())}},
				},
				ok: okDoc{
					description: "The dataset file as published.", media: "application/json", raw: schema{"type": "object"},
					headers: datasetHeaders(),
				},
				notModified: "The `If-None-Match` names the served version: no body.",
				errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError},
			},
		},
		{
			method: http.MethodGet, path: "/openapi.yaml", when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveOpenAPIYAML,
			cache:  cacheDocs,
			doc: &opDoc{
				id: "openapiYaml", tag: "meta", summary: "This API description (YAML)",
				description: "The OpenAPI 3.1 document of this service, generated from its route table and tool registry.",
				ok:          okDoc{description: "The OpenAPI document.", media: "application/yaml", raw: schema{"type": "string"}},
				errors:      []int{http.StatusTooManyRequests},
			},
		},
		{
			method: http.MethodGet, path: "/openapi.json", when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveOpenAPIJSON,
			cache:  cacheDocs,
			doc: &opDoc{
				id: "openapiJson", tag: "meta", summary: "This API description (JSON)",
				description: "The same OpenAPI 3.1 document as JSON.",
				ok:          okDoc{description: "The OpenAPI document.", media: "application/json", raw: schema{"type": "object"}},
				errors:      []int{http.StatusTooManyRequests},
			},
		},
		{
			method: http.MethodGet, path: "/{$}", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveIndex,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsRedirect,
		},
		{
			method: http.MethodGet, path: "/docs/{$}", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/claude-code", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/mcp", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/rest", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/tools", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/access", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/attribution", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/terms", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsPage,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/tools/{tool}", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsTool,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/docs/search.json", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).docsSearch,
			cache:  cacheDocs,
		},
		{
			// Versioned static files of the portal: immutable, so no per-caller limit.
			method: http.MethodGet, path: "/assets/{version}/{file}", root: true, when: docsOn, rate: unlimited, trialFree: true,
			handle: (*API).asset,
		},
		{
			method: http.MethodGet, path: "/terms", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveTerms,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/terms.md", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveTermsMarkdown,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/llms.txt", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveLLMs,
			cache:  cacheDocs,
		},
		{
			method: http.MethodGet, path: "/llms-full.txt", root: true, when: docsOn,
			rate: func(s Surface) Rate { return s.Limits.Docs }, trialFree: true,
			handle: (*API).serveLLMsFull,
			cache:  cacheDocs,
		},
	}
}
