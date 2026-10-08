// Package mcpserver exposes the deterministic EVE-Cyno tools as a Model Context Protocol
// server (roadmap R3.3): the same registry (core/catalog) and the same executor
// (tools.ExecuteToolResult) as the REST tool endpoint, so a tool answers identically over
// MCP, REST and the chat brain.
//
// It is a library: cmd/mcp serves it over stdio for local clients (Claude Desktop, Claude
// Code), and cmd/dataapi mounts the streamable-HTTP handler (HTTPHandler) at /v1/mcp
// behind the data API's request IDs and rate limiter.
//
// What a client sees:
//
//   - tools/list: one MCP tool per registry tool, with the tool_schemas.json description
//     and argument schema as is. The set follows the tool tiers (catalog.Tier). Over
//     HTTP it is chosen per request: the public tools always, the keyed tools only when
//     the caller presented a valid API key (HTTPOptions.Keyed), the byo-key tool always
//     (its description names the X-Janice-Key header; a call without the header is an
//     isError result saying so) and the disabled tools never; a tool that is not listed
//     is the protocol's own "unknown tool" error. Over stdio the default is the public
//     tools and Options.All registers every tool, for a local process that spends its
//     owner's keys.
//   - tools/call: content[0] is the LLM-oriented text exactly as tools.ExecuteTool
//     returns it; content[1], when the tool has upstreams, is a short "Sources:" line
//     (attribution is a condition of several of the licences, and most clients do not
//     show _meta to the model); structuredContent is the typed result where the tool has
//     one; _meta carries the full attribution (name, url, licence) and the build version.
//   - A tool failure (upstream error, timeout, bad arguments) is a normal result with
//     isError set and the message as text, so the model can read it and retry; only a
//     malformed request (arguments that are not a JSON object) or an unregistered tool is
//     a JSON-RPC error.
//
// Arguments are not validated against the schema (parity with the REST tool endpoint):
// the tools tolerate missing and mistyped arguments and say so in their text.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
	"eve-cyno.dev/go/data/version"
)

const (
	// DefaultTimeout bounds one tool execution (the budget of the REST tool endpoint).
	DefaultTimeout = 30 * time.Second

	// MetaPrefix namespaces the keys this server adds to a result's _meta.
	MetaPrefix = "eve-cyno/"

	// ServerName is the implementation name announced in the initialize handshake.
	ServerName = "eve-cyno"

	// httpBodyMax caps a streamable-HTTP request body, like the REST tool endpoint.
	httpBodyMax = 64 << 10

	// maxJaniceKeyLen caps the X-Janice-Key header; a longer value is ignored.
	maxJaniceKeyLen = 256
)

// instructions is what the initialize result tells the client about the server.
const instructions = "Deterministic EVE Online data and calculation tools: SDE lookups (items, ships, skills, " +
	"stations, routes), fit validation and stats, market prices, sovereignty and system activity. " +
	"Answers are computed from CCP's Static Data Export and ESI, not generated. " +
	"EVE Online and its data are the property of CCP Games / Fenris Creations; this service is " +
	"non-commercial and unaffiliated (EVE Developer License Agreement). Each answer names its sources."

// Options configures a server.
type Options struct {
	// All registers every tool, whatever its tier (keyed, byo-key and disabled ones
	// included; they use the process's own keys). Only for a process that serves its own
	// user (cmd/mcp -all); never for an internet-facing one.
	All bool
	// Timeout bounds one tool execution; zero means DefaultTimeout.
	Timeout time.Duration
	// Logger receives the server's diagnostics (a recovered panic, a failed tool). Nil
	// discards. A stdio server must never log to stdout: that is the protocol channel.
	Logger *slog.Logger
}

// Exposed returns the tools a stdio-style server built with opts registers, in registry
// order: everything with All, else the public tier.
func Exposed(opts Options) []catalog.Tool {
	if opts.All {
		return catalog.Tools()
	}
	return catalog.ToolsIn(catalog.TierPublic)
}

// executor runs one tool call; tools.ExecuteToolResult in production, a fake in tests.
type executor func(ctx context.Context, deps *tools.Deps, name string, args map[string]any) (tools.Result, error)

// New builds a server over deps. deps may be nil (the SDE did not load): the tools are
// still listed and every call answers an isError result saying the layer is unavailable.
func New(deps *tools.Deps, opts Options) *mcp.Server {
	return newServer(deps, opts, tools.ExecuteToolResult)
}

func newServer(deps *tools.Deps, opts Options, exec executor) *mcp.Server {
	return buildServer(deps, Exposed(opts), serverConfig{timeout: opts.Timeout, log: opts.Logger}, exec)
}

// serverConfig is what buildServer needs beyond the tool list.
type serverConfig struct {
	timeout time.Duration
	log     *slog.Logger
	// janiceFromHeader marks a per-request HTTP server: byo-key tools run with the
	// caller's own key (already in the deps) and are refused without one.
	janiceFromHeader bool
}

// buildServer registers exposed on a new server over deps.
func buildServer(deps *tools.Deps, exposed []catalog.Tool, cfg serverConfig, exec executor) *mcp.Server {
	log := cfg.log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	timeout := cfg.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    ServerName,
			Title:   "EVE-Cyno data tools",
			Version: version.Version(),
		},
		&mcp.ServerOptions{Instructions: instructions, Logger: quiet(log)},
	)
	h := &handler{deps: deps, exec: exec, timeout: timeout, log: log, janiceFromHeader: cfg.janiceFromHeader}
	for _, t := range exposed {
		s.AddTool(mcpTool(t, cfg.janiceFromHeader), h.call(t))
	}
	return s
}

// janiceNote is appended to the description of a byo-key tool on a per-request HTTP server.
const janiceNote = " Requires your own Janice API key in the " + catalog.HeaderJaniceKey + " request header; this service never uses its own."

// mcpTool maps a registry tool to its MCP definition. janiceHeader adds the byo-key note.
func mcpTool(t catalog.Tool, janiceHeader bool) *mcp.Tool {
	no, openWorld := false, false
	for _, src := range t.Attribution {
		if src.Name != "" && !strings.Contains(src.Name, "Static Data Export") {
			openWorld = true // reaches beyond the local SDE: ESI, zKillboard, ...
		}
	}
	desc := t.Description
	if janiceHeader && t.Tier == catalog.TierBYOKey {
		desc += janiceNote
	}
	return &mcp.Tool{
		Name:        t.Name,
		Description: desc,
		InputSchema: t.Parameters,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &no,
			OpenWorldHint:   &openWorld,
		},
	}
}

type handler struct {
	deps    *tools.Deps
	exec    executor
	timeout time.Duration
	log     *slog.Logger
	// janiceFromHeader: deps carries the caller's Janice key ("" when it sent none).
	janiceFromHeader bool
}

// call returns the tools/call handler of one tool.
func (h *handler) call(t catalog.Tool) mcp.ToolHandler {
	name := t.Name
	return func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		// A panic in a tool must not take the process down: the SDK runs handlers on its own
		// goroutines, outside net/http's per-request recover.
		defer func() {
			if p := recover(); p != nil {
				h.log.Error("mcp_tool_panic", "tool", name, "panic", p, "stack", string(debug.Stack()))
				res, err = failure("internal error while running "+name), nil
			}
		}()

		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &args); err != nil || args == nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "arguments must be a JSON object"}
			}
		}
		if h.deps == nil {
			return failure("deterministic layer unavailable (SDE not loaded)"), nil
		}
		if h.janiceFromHeader && t.Tier == catalog.TierBYOKey && h.deps.JaniceAPIKey == "" {
			return failure(name + " runs with your own Janice API key: send it in the " + catalog.HeaderJaniceKey + " request header"), nil
		}

		ctx, cancel := context.WithTimeout(ctx, h.timeout)
		defer cancel()
		out, err := h.exec(ctx, h.deps, name, args)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return failure(fmt.Sprintf("%s timed out after %s", name, h.timeout)), nil
			}
			h.log.Warn("mcp_tool_failed", "tool", name, "error", err)
			return failure(err.Error()), nil
		}
		return success(out), nil
	}
}

// failure is a tool-level error: isError with the message as text.
func failure(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// success maps a tools.Result to a tools/call result (see the package doc).
func success(r tools.Result) *mcp.CallToolResult {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: r.Text}},
		Meta: mcp.Meta{
			MetaPrefix + "tool":        r.Tool,
			MetaPrefix + "version":     r.Version,
			MetaPrefix + "attribution": nonNilSources(r.Attribution),
		},
	}
	if line := sourcesLine(r.Attribution); line != "" {
		res.Content = append(res.Content, &mcp.TextContent{Text: line})
	}
	if r.Data != nil {
		res.StructuredContent = r.Data
	}
	return res
}

// sourcesLine renders the attribution as one line of text for the model.
func sourcesLine(src []tools.Source) string {
	if len(src) == 0 {
		return ""
	}
	parts := make([]string, 0, len(src))
	for _, s := range src {
		parts = append(parts, s.Name+" <"+s.URL+">")
	}
	return "Sources: " + strings.Join(parts, "; ")
}

func nonNilSources(s []tools.Source) []tools.Source {
	if s == nil {
		return []tools.Source{}
	}
	return s
}

// quiet returns a logger that passes only warnings and errors of l on to the SDK: it logs
// a session connect / end at info level, which in stateless mode is two lines for every
// request.
func quiet(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(minLevel{Handler: l.Handler(), min: slog.LevelWarn})
}

type minLevel struct {
	slog.Handler
	min slog.Level
}

func (m minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= m.min && m.Handler.Enabled(ctx, l)
}

func (m minLevel) WithAttrs(a []slog.Attr) slog.Handler {
	return minLevel{Handler: m.Handler.WithAttrs(a), min: m.min}
}

func (m minLevel) WithGroup(name string) slog.Handler {
	return minLevel{Handler: m.Handler.WithGroup(name), min: m.min}
}

// HTTPOptions configures HTTPHandler.
type HTTPOptions struct {
	// DisableLocalhostProtection turns off the SDK's DNS-rebinding guard, which answers
	// 403 to a request that reaches a loopback listener with a non-loopback Host header.
	// A service published through a reverse proxy or tunnel that connects over loopback
	// (cloudflared does) needs it off, or every real request is refused; a server only
	// ever reached directly on loopback should leave it on.
	DisableLocalhostProtection bool
	// Keyed reports whether the request's caller may use the keyed tier (it presented a
	// valid API key). Nil means nobody may: only the public and byo-key tiers are served.
	// Authentication itself is the embedding service's job (dataapi authenticates before
	// this handler runs and refuses an invalid key with 401).
	Keyed func(*http.Request) bool
	// Timeout bounds one tool execution; zero means DefaultTimeout.
	Timeout time.Duration
	// Logger receives the transport's and the tools' diagnostics. Nil discards.
	Logger *slog.Logger
}

// HTTPHandler serves the tools over deps on the MCP streamable-HTTP transport, stateless:
// no session is kept between requests (every POST is self-contained and answered as one
// JSON body), so any replica can answer any request and an idle client costs nothing. The
// handler serves one URL; mount it where the clients are pointed (dataapi: {prefix}/mcp).
// GET (the server-initiated stream) and DELETE (end session) are 405 in this mode.
//
// The tool set is chosen per request (see the package doc): public and byo-key tools
// always, keyed tools when o.Keyed says so, disabled tools never. Each request gets its own
// server over its own copy of deps (tools.Deps.WithJaniceKey) that carries the caller's
// X-Janice-Key, or no Janice key at all, never deps' own: the shared deps are not mutated,
// so concurrent callers cannot see each other's keys, and the key is not logged or kept.
func HTTPHandler(deps *tools.Deps, o HTTPOptions) http.Handler {
	return httpHandler(deps, o, tools.ExecuteToolResult)
}

func httpHandler(deps *tools.Deps, o HTTPOptions, exec executor) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			tiers := []catalog.Tier{catalog.TierPublic, catalog.TierBYOKey}
			if o.Keyed != nil && o.Keyed(r) {
				tiers = []catalog.Tier{catalog.TierPublic, catalog.TierKeyed, catalog.TierBYOKey}
			}
			janice := strings.TrimSpace(r.Header.Get(catalog.HeaderJaniceKey))
			if len(janice) > maxJaniceKeyLen {
				janice = "" // not a Janice key; treated as absent
			}
			return buildServer(deps.WithJaniceKey(janice), catalog.ToolsIn(tiers...),
				serverConfig{timeout: o.Timeout, log: o.Logger, janiceFromHeader: true}, exec)
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			Logger:                     quiet(o.Logger),
			DisableLocalhostProtection: o.DisableLocalhostProtection,
			MaxRequestBodyBytes:        httpBodyMax,
		},
	)
}
