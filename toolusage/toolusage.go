// Package toolusage writes the aggregate usage line of the hosted service: one slog INFO
// record per tool call, the same for the REST endpoint and the MCP server, so an operator
// can count which tools are used, over which surface, by which kind of client and with
// what result.
//
// The line is deliberately anonymous. Its fields are the tool name (a catalog name or
// "unknown", never the raw string a caller sent), the surface, the tier, the outcome,
// whether the call spent the live-upstream budget, the duration and a client family from a fixed allowlist.
// It never carries an address, a forwarded-for or proxy header, an API key or key id, an
// account id, an argument value or any part of a result. The record carries no request ID,
// IP, key or caller token.
package toolusage

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

// Message is the log message of the usage line.
const Message = "tool_call"

// Surface is the interface a call came in through.
type Surface string

const (
	// REST is POST /v1/tool/{name}.
	REST Surface = "rest"
	// MCP is a tools/call on the MCP server.
	MCP Surface = "mcp"
)

// Outcome is the coarse result of one call.
type Outcome string

const (
	// OK: the tool answered with a typed result.
	OK Outcome = "ok"
	// TextOnly: the tool answered, but with text and no typed data.
	TextOnly Outcome = "text_only"
	// InvalidArgs: the arguments were malformed or a required one was missing.
	InvalidArgs Outcome = "invalid_args"
	// RateLimited: the live-upstream budget of the caller was spent.
	RateLimited Outcome = "rate_limited"
	// UpstreamBusy: the upstream client refused to send (a rate limit is near).
	UpstreamBusy Outcome = "upstream_busy"
	// Timeout: the tool ran past its time limit.
	Timeout Outcome = "timeout"
	// Failed: any other failure (unknown tool, internal error, layer unavailable).
	Failed Outcome = "error"
	// Unauthorized: the key was invalid, or the tool needs a key or is disabled.
	Unauthorized Outcome = "unauthorized"
)

// Call is what one usage line records.
type Call struct {
	// Tool is the name the caller asked for; Log reduces an unregistered name to "unknown".
	Tool    string
	Surface Surface
	// Keyed is true when the call carried a valid API key.
	Keyed bool
	// Outcome is the result class.
	Outcome Outcome
	// Live is true when the call spent the live-upstream budget.
	Live     bool
	Duration time.Duration
	// Client is the family from Client or ClientFromUserAgent.
	Client string
}

// Log writes the usage line of c to l. A nil l discards it.
func Log(l *slog.Logger, c Call) {
	if l == nil {
		return
	}
	tier := "keyless"
	if c.Keyed {
		tier = "keyed"
	}
	l.LogAttrs(context.Background(), slog.LevelInfo, Message,
		slog.String("tool", ToolName(c.Tool)),
		slog.String("surface", string(c.Surface)),
		slog.String("tier", tier),
		slog.String("outcome", string(c.Outcome)),
		slog.Bool("live", c.Live),
		slog.Int64("duration_ms", c.Duration.Milliseconds()),
		slog.String("client", c.Client),
	)
}

// ToolName is name when it is a catalog tool, else "unknown": the label set stays bounded
// and a caller-supplied string never reaches the logs.
func ToolName(name string) string {
	if _, ok := catalog.Lookup(name); ok {
		return name
	}
	return "unknown"
}

// OutcomeOfError classifies a failed tool execution.
func OutcomeOfError(err error) Outcome {
	switch {
	case err == nil:
		return OK
	case strings.HasPrefix(err.Error(), "unknown tool"):
		return Failed
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout
	}
	if _, busy := tools.UpstreamBusy(err); busy {
		return UpstreamBusy
	}
	return Failed
}

// OutcomeOfResult classifies a successful tool execution.
func OutcomeOfResult(r tools.Result) Outcome {
	if r.Data == nil {
		return TextOnly
	}
	return OK
}

// clientFamily is one entry of the client allowlist: a family label and how a lowercased,
// normalised client name is matched to it.
type clientFamily struct {
	label string
	// prefix and contains are matched against the normalised name (lowercase, spaces and
	// underscores turned into "-"); either one matching selects the family.
	prefix   []string
	contains []string
}

// clientFamilies is the allowlist, checked in order (the first match wins, so the more
// specific claude-* families come first). Only these labels, "other" and "none" are ever
// logged: a client name is free text a caller chooses, and it could carry a token, a
// mailbox or a machine name, so it is mapped to a family and never echoed.
func clientFamilies() []clientFamily {
	return []clientFamily{
		{label: "claude-code", contains: []string{"claude-code", "claudecode"}},
		{label: "claude-desktop", contains: []string{"claude-desktop", "claudedesktop"}},
		{label: "claude-ai", contains: []string{"claude-ai", "claudeai"}},
		{label: "cursor", prefix: []string{"cursor"}},
		{label: "vscode", prefix: []string{"vscode", "visual-studio-code", "vs-code"}, contains: []string{"vscode"}},
		{label: "windsurf", prefix: []string{"windsurf"}},
		{label: "zed", prefix: []string{"zed-", "zed/"}},
		{label: "openai", prefix: []string{"openai", "chatgpt"}},
		{label: "python-requests", prefix: []string{"python-requests"}},
		{label: "python-httpx", prefix: []string{"python-httpx"}},
		{label: "python-urllib", prefix: []string{"python-urllib"}},
		{label: "aiohttp", prefix: []string{"aiohttp"}},
		{label: "curl", prefix: []string{"curl"}},
		{label: "wget", prefix: []string{"wget"}},
		{label: "go-http-client", prefix: []string{"go-http-client"}},
		{label: "node", prefix: []string{"node"}},
		{label: "undici", prefix: []string{"undici"}},
		{label: "axios", prefix: []string{"axios"}},
		{label: "okhttp", prefix: []string{"okhttp"}},
		{label: "java", prefix: []string{"java"}},
		{label: "postman", contains: []string{"postman"}},
		{label: "insomnia", prefix: []string{"insomnia"}},
		{label: "mozilla", prefix: []string{"mozilla"}},
	}
}

// Client maps a client name (an MCP clientInfo.name) to a family of the allowlist: "none"
// for an empty name, "other" for a name no family matches. The name itself is never logged.
func Client(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return "none"
	}
	n = strings.NewReplacer(" ", "-", "_", "-").Replace(n)
	if n == "zed" {
		return "zed"
	}
	for _, f := range clientFamilies() {
		for _, p := range f.prefix {
			if strings.HasPrefix(n, p) {
				return f.label
			}
		}
		for _, c := range f.contains {
			if strings.Contains(n, c) {
				return f.label
			}
		}
	}
	return "other"
}

// ClientFromUserAgent labels a REST caller by the first product token of its User-Agent
// (the text before the first "/" or space), mapped like Client.
func ClientFromUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if i := strings.IndexAny(ua, "/ "); i >= 0 {
		ua = ua[:i]
	}
	return Client(ua)
}
