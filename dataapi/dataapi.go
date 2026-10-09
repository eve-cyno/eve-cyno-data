// Package dataapi is Product A's HTTP face: the deterministic, no-LLM data
// endpoints (community-fit search, fit detail / stats / suggestions, SDE item
// search and the opt-in raw tool API) behind a stdlib http.ServeMux.
//
// It is a library, not a binary: cmd/dataapi serves it standalone on /v1/, and
// services/native mounts the same handlers under its legacy /api/ paths (the
// Prefix option) so the web UI and the desktop app keep working unchanged.
//
// Contract (every route):
//
//   - Errors are JSON, always: {"error":{"code":"...","message":"..."}}. Codes
//     are stable: not_found, method_not_allowed, invalid_request,
//     payload_too_large, rate_limited, unavailable, upstream_error,
//     unknown_tool, tool_disabled, api_key_required, invalid_api_key, key_not_permitted,
//     janice_key_required, timeout, internal_error.
//   - Every response carries X-Request-ID: the caller's value when it is a safe
//     token ([A-Za-z0-9._-], 1-64 chars), otherwise a generated one. The same ID
//     is attached to every log line the request produces.
//   - Per-caller rate limits (sliding window, one limiter per route). The caller
//     is named by a KeyResolver; the default keys an authenticated caller on its API
//     key id and an anonymous one on the client IP, taken from Cloudflare's
//     CF-Connecting-IP header and falling back to the TCP peer, never from
//     X-Forwarded-For / X-Real-IP. A refused request is a 429 with a
//     Retry-After header. CF-Connecting-IP is trusted as-is, so the service must
//     only be reachable through the Cloudflare tunnel (or loopback).
//   - Request bodies are capped (256 KiB for the fit routes, 64 KiB for the tool
//     API); an oversize body is a 413.
//   - Tool tiers (R3.4, core/catalog): on an internet-facing service a tool is public
//     (anyone), keyed (a valid API key, from Config.Auth: Authorization: Bearer or
//     X-API-Key), byo-key (appraise_items: the caller's own Janice key in X-Janice-Key,
//     used for that request only and never logged or stored) or disabled. The same
//     policy decides the REST tool endpoint and the MCP tool list per request.
//   - The tool API answers a JSON envelope (Config.ToolFormat), except where a
//     legacy mount asks for the bare text.
//   - Optional machine-readable surfaces, all generated from the same route table and
//     tool registry (core/catalog): Config.Docs serves GET {prefix}/openapi.yaml and
//     openapi.json plus /llms.txt and /llms-full.txt at the root of the host;
//     Config.MCP mounts an MCP streamable-HTTP handler at {prefix}/mcp behind the same
//     request IDs and limiter. Both are off by default: cmd/dataapi turns them on, the
//     native "/api" mount does not (its paths and tool responses are legacy, and the
//     root-level files are not its to serve). Config.Docs also serves the human/developer
//     index at GET / (HTML, or JSON for Accept: application/json) and the service terms at
//     GET /terms (HTML) and /terms.md.
//   - Cache-Control: descriptions, index and terms are public for an hour, pure-SDE reads
//     (items search) for five minutes, and /health, every POST and the MCP endpoint are
//     no-store. Only a 200 is cacheable; every error is no-store.
//
// Dependencies arrive through Config; the package has no globals and imports
// only Product A packages (enforced by core/internal/tools/importcheck).
package dataapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

const (
	// DefaultPrefix is the route prefix of the public API.
	DefaultPrefix = "/v1"

	// DefaultToolTimeout bounds one tool execution (the budget the retired
	// cmd/publicapi used).
	DefaultToolTimeout = 30 * time.Second

	// fitBodyMax caps the JSON bodies of the fit routes (an EFT block plus a few
	// flags is a few KiB; this is generous).
	fitBodyMax = 256 << 10
	// toolBodyMax caps the tool API body. Tool arguments are a few short strings;
	// the largest legitimate payload (an appraise_items item list) is far below it.
	toolBodyMax = 64 << 10
)

// FitSearcher is the community-fit corpus the search and suggestion routes read.
// *rag.QdrantRetriever implements it.
type FitSearcher interface {
	SearchFits(ctx context.Context, q rag.FitSearchQuery) (rag.FitSearchResult, error)
}

// Deps are the collaborators the handlers read. Any of them may be nil; the
// routes that need a missing one answer 503 `unavailable` instead of failing at
// startup, so a service with an SDE but no corpus still serves what it can.
//
// Pass untyped nil, never a typed nil pointer wrapped in an interface (a nil
// *rag.QdrantRetriever assigned to Retriever is a non-nil interface).
type Deps struct {
	// Tools is the deterministic tool layer; Tools.SDE is the one SDE every route
	// reads (item search, hull-name resolution, EFT parsing).
	Tools *tools.Deps
	// Retriever backs /fits/search and the community frequencies of /fit/suggest.
	Retriever FitSearcher
	// Stats computes /fit/stats (core/fit/gofa).
	Stats fit.StatsProvider
}

// Rate is one sliding-window budget per caller key: at most Max requests in any
// Window. A Max or Window <= 0 disables the limit (the zero Rate is unlimited).
type Rate struct {
	Max    int
	Window time.Duration
}

// Limits holds the per-route budgets.
type Limits struct {
	FitsSearch  Rate
	FitsDetail  Rate
	FitStats    Rate
	FitSuggest  Rate
	ItemsSearch Rate
	// Tool applies only in ToolAPIPublic mode.
	Tool Rate
	// MCP counts every request to the MCP endpoint (a session is a handful of requests
	// plus one per tool call).
	MCP Rate
	// Docs covers the description routes (OpenAPI, llms.txt).
	Docs Rate
}

// DefaultLimits are the budgets the endpoints had inside services/native.
func DefaultLimits() Limits {
	return Limits{
		FitsSearch:  Rate{Max: 120, Window: time.Minute}, // read-only + cheap
		FitsDetail:  Rate{Max: 60, Window: time.Minute},  // a few SDE + optional Janice round-trips
		FitStats:    Rate{Max: 60, Window: time.Minute},  // EFT parse + Gofa resolver
		FitSuggest:  Rate{Max: 60, Window: time.Minute},  // RAG scroll + SDE pool
		ItemsSearch: Rate{Max: 120, Window: time.Minute}, // one indexed SDE query
		Tool:        Rate{Max: 30, Window: time.Minute},  // raw tools, internet-facing only
		MCP:         Rate{Max: 60, Window: time.Minute},  // handshake (3 requests) + tool calls
		Docs:        Rate{Max: 60, Window: time.Minute},  // static bytes, but anonymous
	}
}

// ToolAPIMode controls the raw tool endpoint POST {prefix}/tool/{name}.
type ToolAPIMode int

const (
	// ToolAPIOff (the zero value) does not register the route at all: it answers
	// 404 like any unknown path. The endpoint runs raw tools for any caller, so it
	// is opt-in.
	ToolAPIOff ToolAPIMode = iota
	// ToolAPIPublic registers the route for an internet-facing service: the Tool
	// rate limit applies and the tool tiers are enforced (catalog.Tier): keyed tools
	// need an API key (401 api_key_required / invalid_api_key), appraise_items needs
	// the caller's own Janice key in X-Janice-Key (400 janice_key_required), disabled
	// tools answer 403 tool_disabled. The project's Janice key is never used.
	ToolAPIPublic
	// ToolAPILoopback registers the route with no limit and no tiers, for a
	// process that only serves its own user (the desktop app).
	ToolAPILoopback
)

// ToolFormat is the 200 body of the raw tool endpoint POST {prefix}/tool/{name}.
type ToolFormat int

const (
	// ToolFormatEnvelope (the zero value) answers application/json:
	//
	//	{"tool": "...", "version": "...",
	//	 "result": {"text": "...", "data": {...} | null},
	//	 "attribution": [{"name": "...", "url": "...", "license": "..."}]}
	//
	// result.text is the LLM-oriented text the brain sees; result.data is the typed
	// result for the tools that have one (null for the rest); attribution names the
	// upstreams behind the answer. This is the public /v1 contract.
	ToolFormatEnvelope ToolFormat = iota
	// ToolFormatText answers the bare text, text/plain: the contract of the legacy
	// native /api/tool/{name}, which the desktop app and any script written against it
	// read as is. services/native sets it on its "/api" mount; nothing new should.
	ToolFormatText
)

// Config configures New.
type Config struct {
	Deps Deps

	// Prefix is the route prefix; empty means DefaultPrefix ("/v1"). It must start
	// with "/" and must not end with one. services/native sets "/api" to keep its
	// legacy paths.
	Prefix string

	// Limits are the per-route budgets; nil means DefaultLimits().
	Limits *Limits

	// Keys names the limiter bucket of a request; nil means CallerKeys (the key id of
	// an authenticated caller, else the client IP).
	Keys KeyResolver

	// Auth turns an API key into a Caller (see Authenticator, StaticKeys). Nil means no
	// keys exist: every caller is anonymous and keyed tools answer 401 api_key_required.
	// It applies to the tool endpoint and, through CallerFrom, to the MCP handler.
	Auth Authenticator

	// CorpusProbeTTL is how long /health reuses a corpus reachability verdict; zero
	// means DefaultCorpusProbeTTL. Only a Retriever that implements Pinger is probed.
	CorpusProbeTTL time.Duration

	// ToolAPI selects the raw tool endpoint mode; zero is off.
	ToolAPI ToolAPIMode

	// ToolFormat selects the body of a successful tool call; zero is the JSON
	// envelope. Errors are the JSON error shape in either format.
	ToolFormat ToolFormat

	// ToolTimeout bounds one tool execution; zero means DefaultToolTimeout.
	ToolTimeout time.Duration

	// Docs serves the generated descriptions of the API: GET {prefix}/openapi.yaml and
	// {prefix}/openapi.json, and GET /llms.txt and /llms-full.txt at the root of the
	// host (so the service must own the host's root, as cmd/dataapi does; a mount
	// under a sub-path never sees those two). They describe this service's own
	// configuration: the tool endpoint only when ToolAPI is on, the MCP endpoint only
	// when MCP is set. New fails if Docs is combined with ToolFormatText, which they do
	// not describe.
	Docs bool

	// PublicURL is the origin the service is reachable at, e.g. "https://data.eve-cyno.dev"
	// (https, no path or query; New fails otherwise). When set, the generated OpenAPI
	// `servers` entry and the links of llms.txt / llms-full.txt are absolute, which clients
	// that cannot resolve relative server URLs (ChatGPT Actions) need, and the index page
	// shows it in its examples. Empty keeps everything relative.
	PublicURL string

	// MCP, when non-nil, is mounted at {prefix}/mcp for every HTTP method, behind the
	// request ID, panic recovery, API-key authentication (an invalid key is a 401 before
	// the handler runs) and the Limits.MCP limiter. Build it with mcpserver.HTTPHandler,
	// which picks the tool set per request; give it Keyed: func(r) bool { return
	// CallerFrom(r.Context()).Allows(catalog.TierKeyed) }. Pass untyped nil, never a typed
	// nil pointer wrapped in the interface.
	MCP http.Handler

	// Logger receives the access log and handler diagnostics; every line carries
	// request_id. Nil discards.
	Logger *slog.Logger
}

// API is the data API. It implements http.Handler.
type API struct {
	deps        Deps
	prefix      string
	keys        KeyResolver
	auth        Authenticator
	toolMode    ToolAPIMode
	toolFormat  ToolFormat
	toolTimeout time.Duration
	mcp         http.Handler
	surface     Surface
	docs        renderedDocs
	pages       renderedPages
	log         *slog.Logger
	mux         *http.ServeMux
	// corpusProbe is nil unless Deps.Retriever implements Pinger.
	corpusProbe *corpusProbe
}

// New builds the API and its routes. It fails on an invalid Prefix, an unknown mode, or a
// combination of options that cannot be served (see Config.Docs).
func New(cfg Config) (*API, error) {
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "{}") {
		return nil, fmt.Errorf("dataapi: invalid prefix %q (want a path like %q)", prefix, DefaultPrefix)
	}
	switch cfg.ToolAPI {
	case ToolAPIOff, ToolAPIPublic, ToolAPILoopback:
	default:
		return nil, errors.New("dataapi: unknown ToolAPI mode")
	}
	switch cfg.ToolFormat {
	case ToolFormatEnvelope, ToolFormatText:
	default:
		return nil, errors.New("dataapi: unknown ToolFormat")
	}

	if cfg.Docs && cfg.ToolFormat == ToolFormatText {
		return nil, errors.New("dataapi: Docs describes the JSON tool envelope and cannot be combined with ToolFormatText")
	}

	publicURL, err := ValidatePublicURL(cfg.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("dataapi: %w", err)
	}

	limits := DefaultLimits()
	if cfg.Limits != nil {
		limits = *cfg.Limits
	}
	a := &API{
		deps:        cfg.Deps,
		prefix:      prefix,
		keys:        cfg.Keys,
		auth:        cfg.Auth,
		toolMode:    cfg.ToolAPI,
		toolFormat:  cfg.ToolFormat,
		toolTimeout: cfg.ToolTimeout,
		mcp:         cfg.MCP,
		log:         cfg.Logger,
		mux:         http.NewServeMux(),
	}
	if p, ok := cfg.Deps.Retriever.(Pinger); ok && p != nil {
		a.corpusProbe = newCorpusProbe(p, cfg.CorpusProbeTTL)
	}
	a.surface = Surface{Prefix: prefix, ToolAPI: cfg.ToolAPI, MCP: cfg.MCP != nil, Docs: cfg.Docs, Auth: cfg.Auth != nil, Limits: limits, PublicURL: publicURL}
	if a.keys == nil {
		a.keys = CallerKeys{}
	}
	if a.toolTimeout <= 0 {
		a.toolTimeout = DefaultToolTimeout
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}

	for _, rt := range routeTable() {
		if rt.when == nil || rt.when(a.surface) {
			a.register(rt)
		}
	}
	if cfg.Docs {
		docs, err := renderDocs(a.surface)
		if err != nil {
			return nil, fmt.Errorf("dataapi: render the API description: %w", err)
		}
		a.docs = docs
		a.pages = renderPages(a.surface)
	}
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such route")
	})
	return a, nil
}

// register mounts one entry of the route table: the method-specific pattern behind its
// limiter, plus a method-less fallback for the same path so a wrong method is a JSON 405
// with an Allow header (the stdlib mux would answer with a plain-text body). A route
// with anyMethod hands every method to its handler instead.
func (a *API) register(rt route) {
	full := rt.path
	if !rt.root {
		full = a.prefix + rt.path
	}
	h := cached(rt.cache, a.limited(limiterFor(rt.rate(a.surface)), func(w http.ResponseWriter, r *http.Request) {
		rt.handle(a, w, r)
	}))
	if rt.anyMethod {
		a.mux.HandleFunc(full, h)
		return
	}
	a.mux.HandleFunc(rt.method+" "+full, h)
	a.mux.HandleFunc(full, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", rt.method)
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed; use "+rt.method)
	})
}

// sde returns the SDE the handlers read, or nil when none was provided.
func (a *API) sde() *sde.SDE {
	if a.deps.Tools == nil {
		return nil
	}
	return a.deps.Tools.SDE
}
