// Command dataapi serves the HTTP data API (dataapi) on its own:
// community-fit search, fit detail / stats / suggestions, SDE item search and the
// opt-in raw tool API, all deterministic and LLM-free.
//
// Endpoints (JSON errors, X-Request-ID on every response, per-IP rate limits):
//
//	GET  /v1/health
//	GET  /v1/fits/search     POST /v1/fits/detail
//	POST /v1/fit/stats       POST /v1/fit/suggest
//	GET  /v1/items/search
//	POST /v1/tool/{name}     only with DATAAPI_EXPOSE_TOOL_API=true; answers the
//	                         {tool, version, result: {text, data}, attribution} envelope
//	POST /v1/mcp             only with DATAAPI_EXPOSE_MCP=true; the tools over MCP
//	                         streamable HTTP (stateless, JSON responses), chosen per request
//	GET  /v1/openapi.yaml    the generated API description (also /v1/openapi.json)
//	GET  /llms.txt           the llmstxt.org index and /llms-full.txt, the full reference
//	GET  /                   the index page (HTML; JSON with Accept: application/json)
//	GET  /terms, /terms.md   the service terms
//
// The tool endpoint and the MCP endpoint apply the same tool tiers (catalog): public
// tools for anyone (the SDE lookups and the ESI pass-throughs, the latter with a lower
// per-caller limit), keyed tools (analyze_battle)
// only with a valid API key, appraise_items with the caller's optional Janice key in the
// X-Janice-Key header (never the project's), and disabled tools (convert_isk_to_real) never.
// An API key is sent as "Authorization: Bearer <key>" or "X-API-Key: <key>". Each document
// describes exactly the endpoints this process serves.
//
// Configuration comes from the process environment (it does not read a .env
// file; for a local run: `set -a; . ./.env; set +a`):
//
//	DATAAPI_ADDR              listen address (default :8092)
//	DATAAPI_EXPOSE_TOOL_API   register POST /v1/tool/{name} (default false; when on, tool
//	                          tiers are enforced; 10 req/min per caller without an API key,
//	                          60 with one)
//	DATAAPI_EXPOSE_MCP        mount POST /v1/mcp (default false; 20 req/min per caller
//	                          without an API key, 120 with one)
//	DATAAPI_API_KEYS_FILE     path of the API key file: one "id:sha256hex" per line (only
//	                          the SHA-256 of a key is stored; `printf %s "$KEY" | sha256sum`),
//	                          # comments allowed. Unset means no file keys; self-serve keys from
//	                          DATAAPI_KEYS_DB still authenticate. A keyed caller is rate limited
//	                          per key id.
//	DATAAPI_SDE_ONLY          true runs without the community-fit corpus: no Qdrant client is
//	                          built or contacted, fit search answers 503 "unavailable",
//	                          /v1/health reports corpus:false and get_fits / list_fits say the
//	                          corpus is not configured. Unset (default): Qdrant is expected; if
//	                          it is configured but down, fit search answers 502 "upstream_error"
//	                          per request and /v1/health still reports corpus:true
//	DATAAPI_UPSTREAM_TOOL_LIMIT  calls per minute per caller without an API key of the tools that
//	                          read live data (get_market_price, get_type_info,
//	                          get_sovereignty, get_system_activity, get_market_history from
//	                          ESI, plan_jump_route from ESI only when avoid_incursions or
//	                          live_info is set, get_production_chain from ESI only when
//	                          system is set, get_thera_connections from EvE-Scout),
//	                          REST and MCP together
//	                          (default 5; a caller with a key gets twice as many; 0 = no extra
//	                          limit)
//	DATAAPI_ESI_GUARD_PERCENT share (0-90, default 25) of every ESI rate-limit group's tokens
//	                          kept unspent; below it a tool answers 503 upstream_busy instead
//	                          of calling ESI. Every ESI call is logged as msg "esi_call"
//	DATAAPI_LOG_SALT          secret of the opaque "caller" token in the tool/MCP access-log
//	                          lines; tokens rotate at 00:00 UTC, so they compare only within one
//	                          UTC day (with it set, also across restarts within the day). No
//	                          default: unset or blank means a random secret per process, noted
//	                          once at startup; a secret under 32 bytes (trimmed) is used with a
//	                          startup warning. The value is never logged; set it only in the
//	                          host .env (openssl rand -hex 32), since whoever holds it can
//	                          reverse a token
//	DATAAPI_ANTHROPIC_RANGES  comma-separated CIDR prefixes; an access line whose client address
//	                          (the one the limits key on) lies in one carries range="anthropic",
//	                          never the address. Unset: Anthropic's two outbound IPv4 ranges
//	                          160.79.104.0/21,153.61.208.0/20 (no outbound IPv6; source
//	                          https://platform.claude.com/docs/en/api/ip-addresses; the compose
//	                          default too); set but
//	                          blank: no label; an invalid item is a startup error. Every access
//	                          line whose client address parses also carries ipf (v4, v6, or
//	                          pseudo for class E, Cloudflare's pseudo-IPv4)
//	ESI_CONTACT               contact for the ESI User-Agent (esi)
//	DATAAPI_PUBLIC_URL        the public origin, e.g. https://data.eve-cyno.dev (https, no path or
//	                          query; invalid is a startup error). Makes the OpenAPI servers entry
//	                          and the llms.txt links absolute (ChatGPT Actions need that); unset
//	                          keeps them relative
//	DATAAPI_KEYS_DB           path of the self-serve key store (a SQLite file, created if
//	                          missing). Keys issued through the admin API authenticate next to
//	                          the key file, count against a daily quota and have their requests
//	                          recorded per endpoint. Unset: no self-serve keys (key file only)
//	DATAAPI_KEY_DAILY_CAP     daily request cap copied into each newly issued key (integer 0-10000000,
//	                          default 10000; 0 = no cap); the day is the UTC calendar day
//	DATAAPI_KEYED_LIMIT_FACTOR  multiple of the route limits a keyed caller gets on the routes
//	                          that have no explicit keyed figure of their own (integer 1-10,
//	                          default 2); MCP, the tool route, fit search and the live-ESI
//	                          tools have explicit trial and keyed numbers (see
//	                          dataapi.DefaultLimits) that the factor does not change
//	DATAAPI_ANON_DAILY_CAP    requests per UTC day a client without an API key may make (integer
//	                          0-10000000, default 200; 0 = no cap). In memory, per client IP;
//	                          the documentation pages and /health are not counted. Applies only
//	                          where keys exist (a key file or DATAAPI_KEYS_DB)
//	DATAAPI_MAX_INFLIGHT      requests each route runs at the same time (integer 0-10000, default
//	                          0 = no cap); the documentation pages, the portal assets, /health and
//	                          GET/DELETE on /v1/mcp are not capped. Over it the route answers 503
//	                          busy with Retry-After: 1 at once instead of queueing; the check runs
//	                          before every limit, the daily cap and a key's quota, so a busy
//	                          answer charges nothing. A full route first runs three checks that
//	                          record nothing: the per-IP route limiter, the anonymous daily
//	                          cap and the key's quota; a caller already over one of them gets
//	                          its 429 with the real Retry-After instead. The upstream limiter
//	                          is deliberately not pre-checked, so a caller that only the
//	                          upstream limiter would refuse still gets 503 busy
//	DATAAPI_MAX_INFLIGHT_UPSTREAM  a second pool per route for the requests that wait on a third
//	                          party (integer 0-10000, default 0 = no split): on /v1/mcp and
//	                          /v1/tool/{name} a call of a live tool (or a live option, a Janice key,
//	                          a slow upstream tool), a free-text fit search, a fit detail priced by
//	                          Janice. They take these slots instead of DATAAPI_MAX_INFLIGHT ones,
//	                          so calls hung on a slow upstream cannot starve the SDE calls, and the
//	                          reverse. No effect without DATAAPI_MAX_INFLIGHT
//	DATAAPI_ADMIN_TOKEN       bearer token of the admin API that issues, revokes and audits
//	                          keys. Unset: no admin listener. Never logged
//	DATAAPI_ADMIN_ADDR        admin listen address (default 127.0.0.1:8093; the container
//	                          setup sets :8093 so the internal network reaches it); served only
//	                          when both the key store and the admin token are set. The token must
//	                          be at least 32 characters, else startup fails
//	EVE_CORE_*, QDRANT_*, DEEPINFRA_EMBED_*   the deterministic layer (config)
//
// The SDE build id that /v1/health reports is read from the file "<EVE_CORE_SDE_PATH>.build"
// (trimmed; a missing file leaves it empty).
//
// Client IPs for the limiters come from Cloudflare's CF-Connecting-IP header and
// fall back to the TCP peer, so the service must only be reachable through the
// Cloudflare tunnel or on loopback. For the same reason the MCP transport's
// DNS-rebinding guard (it refuses a loopback connection that carries a foreign Host
// header, which is exactly what the tunnel's connector sends) is switched off.
// The service runs behind a Cloudflare tunnel that routes only the main listener. The admin
// listener must never be routed by it: it must be reachable only on the private network
// (never publish its port beyond loopback, never route it through the tunnel); its refusal
// of requests that carry CF-Connecting-IP only stops tunnelled ones.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"eve-cyno.dev/go/data/bootstrap"
	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/sde"
)

const (
	defaultAddr      = ":8092"
	defaultAdminAddr = "127.0.0.1:8093"

	// minAdminTokenLen is the shortest DATAAPI_ADMIN_TOKEN the admin listener starts with.
	minAdminTokenLen = 32

	// shutdownBudget is the graceful-shutdown deadline; it stays under the 10 s default
	// stop timeout of docker so the usage counters are flushed before a SIGKILL.
	shutdownBudget = 8 * time.Second
)

// checkAdminToken refuses a DATAAPI_ADMIN_TOKEN too short to resist guessing. An empty
// token is fine (no admin listener). The error names the variable, never the value.
func checkAdminToken(token string) error {
	if token != "" && len(token) < minAdminTokenLen {
		return fmt.Errorf("DATAAPI_ADMIN_TOKEN: must be at least %d characters", minAdminTokenLen)
	}
	return nil
}

// adminAddr resolves DATAAPI_ADMIN_ADDR.
func adminAddr(getenv func(string) string) string {
	if v := getenv("DATAAPI_ADMIN_ADDR"); v != "" {
		return v
	}
	return defaultAdminAddr
}

// keyedFactor resolves DATAAPI_KEYED_LIMIT_FACTOR: an integer from 1 to 10.
func keyedFactor(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_KEYED_LIMIT_FACTOR"))
	if v == "" {
		return dataapi.DefaultKeyedLimitFactor, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 10 {
		return 0, fmt.Errorf("DATAAPI_KEYED_LIMIT_FACTOR: want 1-10, got %q", v)
	}
	return n, nil
}

// anonDailyCap resolves DATAAPI_ANON_DAILY_CAP: an integer 0..keys.MaxDailyCap (0 = no cap).
func anonDailyCap(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_ANON_DAILY_CAP"))
	if v == "" {
		return dataapi.DefaultAnonDailyCap, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > keys.MaxDailyCap {
		return 0, fmt.Errorf("DATAAPI_ANON_DAILY_CAP: want an integer 0-%d, got %q", keys.MaxDailyCap, v)
	}
	return n, nil
}

// maxInFlightLimit is the largest DATAAPI_MAX_INFLIGHT accepted.
const maxInFlightLimit = 10000

// maxInFlight resolves DATAAPI_MAX_INFLIGHT: an integer 0..maxInFlightLimit (0 = no cap, the
// default).
func maxInFlight(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_MAX_INFLIGHT"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxInFlightLimit {
		return 0, fmt.Errorf("DATAAPI_MAX_INFLIGHT: want an integer 0-%d, got %q", maxInFlightLimit, v)
	}
	return n, nil
}

// maxInFlightUpstream resolves DATAAPI_MAX_INFLIGHT_UPSTREAM: an integer 0..maxInFlightLimit (0 =
// no upstream pool, the default).
func maxInFlightUpstream(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_MAX_INFLIGHT_UPSTREAM"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxInFlightLimit {
		return 0, fmt.Errorf("DATAAPI_MAX_INFLIGHT_UPSTREAM: want an integer 0-%d, got %q", maxInFlightLimit, v)
	}
	return n, nil
}

// minLogSaltLen is the shortest DATAAPI_LOG_SALT (in bytes, trimmed) that starts without a
// warning: the random secret of an unset one is 32 bytes too.
const minLogSaltLen = 32

// logSalt resolves DATAAPI_LOG_SALT, the secret of the caller token, and says once which kind
// of secret the tokens use: blank or unset is "" (dataapi then draws a random secret for this
// process). A set secret under minLogSaltLen bytes is still used, with one warning, since a short
// one may be guessed and every token reversed. The value, and its length, are never logged.
func logSalt(getenv func(string) string, log *slog.Logger) string {
	s := strings.TrimSpace(getenv("DATAAPI_LOG_SALT"))
	if s == "" {
		log.Info("caller token: DATAAPI_LOG_SALT unset, using a random secret per process; tokens rotate at 00:00 UTC and at every restart",
			"env", "DATAAPI_LOG_SALT")
		return ""
	}
	log.Info("caller token: secret from DATAAPI_LOG_SALT; tokens rotate at 00:00 UTC", "env", "DATAAPI_LOG_SALT")
	if len(s) < minLogSaltLen {
		log.Warn("caller token: DATAAPI_LOG_SALT is shorter than 32 bytes, so it may be guessed; set one from `openssl rand -hex 32`",
			"env", "DATAAPI_LOG_SALT")
	}
	return s
}

// defaultAnthropicRanges are Anthropic's outbound IPv4 ranges, the addresses its MCP tool calls
// to external servers (claude.ai and Claude Desktop custom connectors) come from; Anthropic
// publishes no outbound IPv6. Source: https://platform.claude.com/docs/en/api/ip-addresses
// (outbound requests). The compose file passes the same default.
const defaultAnthropicRanges = "160.79.104.0/21,153.61.208.0/20"

// anthropicRanges resolves DATAAPI_ANTHROPIC_RANGES, the networks whose access lines carry
// range="anthropic": unset is defaultAnthropicRanges, set but blank is none (no label), anything
// else a comma-separated CIDR list (dataapi.ParseRanges). The error names the variable and the
// position of the bad item, never the value.
func anthropicRanges(lookup func(string) (string, bool)) ([]netip.Prefix, error) {
	v, ok := lookup("DATAAPI_ANTHROPIC_RANGES")
	if !ok {
		v = defaultAnthropicRanges
	}
	ranges, err := dataapi.ParseRanges(v)
	if err != nil {
		return nil, fmt.Errorf("DATAAPI_ANTHROPIC_RANGES: %w", err)
	}
	return ranges, nil
}

// dailyCap resolves DATAAPI_KEY_DAILY_CAP: an integer 0..keys.MaxDailyCap (0 = no cap).
func dailyCap(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_KEY_DAILY_CAP"))
	if v == "" {
		return dataapi.DefaultKeyDailyCap, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > keys.MaxDailyCap {
		return 0, fmt.Errorf("DATAAPI_KEY_DAILY_CAP: want an integer 0-%d, got %q", keys.MaxDailyCap, v)
	}
	return n, nil
}

var sdeBuildID = regexp.MustCompile(`^[0-9A-Za-z_.-]{1,64}$`)

// sdeBuild reads the SDE build id from "<sdePath>.build": at most 64 bytes, trimmed, and only
// letters, digits, "_", "." and "-" (the value is served on the public /v1/health). A missing
// file or path, or anything else, is "" (a read error other than a missing file is logged).
func sdeBuild(sdePath string, log *slog.Logger) string {
	if sdePath == "" {
		return ""
	}
	f, err := os.Open(sdePath + ".build")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn("read SDE build file", "error", err)
		}
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		log.Warn("read SDE build file", "error", err)
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !sdeBuildID.MatchString(id) {
		return ""
	}
	return id
}

// followSDEBuild returns a function that reports the build id of the SDE file the reader is
// serving. The id comes from the "<sdePath>.build" sidecar and is cached; it is re-read when
// the reader's generation or the sidecar's identity changes, but only while the file on disk
// is the file the reader serves. The ingest renames the database into place and writes the
// sidecar right after, yet the reader switches files only at its next due reload check (never,
// when reloading is off), so a sidecar that is ahead of the reader is ignored until the reader
// has caught up; the next call after that picks it up, including a sidecar written after the
// reload. Before the first id is cached, a disk that is ahead of the reader gives "" (the
// served file's build is unknown). Safe for concurrent use.
func followSDEBuild(sdePath string, s *sde.SDE, log *slog.Logger) func() string {
	var (
		mu    sync.Mutex
		gen   uint64
		fi    os.FileInfo
		id    string
		fresh bool
	)
	return func() string {
		if sdePath == "" {
			return ""
		}
		var g uint64
		inSync := true
		if s != nil {
			g = s.Generation() // runs the due reload check before the id is chosen
			served, disk := s.ServedFile(), statOrNil(sdePath)
			inSync = served != nil && disk != nil && sde.SameFile(served, disk)
		}
		cur := statOrNil(sdePath + ".build")
		mu.Lock()
		defer mu.Unlock()
		if !inSync && !fresh {
			// The disk is ahead of the reader and nothing is cached: the sidecar names a file
			// that is not served, and the id of the served one is unknown. Report none, and
			// do not cache that, so the first call after the reader catches up reads it.
			return ""
		}
		if fresh && (!inSync || (g == gen && sameBuildFile(fi, cur))) {
			return id
		}
		gen, fi, fresh = g, cur, true
		id = sdeBuild(sdePath, log)
		return id
	}
}

// statOrNil returns the file info of path, or nil when it cannot be stat'ed.
func statOrNil(path string) os.FileInfo {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return fi
}

// sameBuildFile reports whether two stat results (nil for a missing file) describe the
// same sidecar content: same inode, size and mtime.
func sameBuildFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// adminServer builds the admin listener, or nil when there is no key store or no token.
func adminServer(store *keys.Store, token, addr string, log *slog.Logger) *http.Server {
	if store == nil || token == "" {
		return nil
	}
	return &http.Server{
		Addr:              addr,
		Handler:           keys.AdminHandler(store, token, log),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// listenAddr resolves DATAAPI_ADDR.
func listenAddr(getenv func(string) string) string {
	if v := getenv("DATAAPI_ADDR"); v != "" {
		return v
	}
	return defaultAddr
}

// exposeToolAPI parses DATAAPI_EXPOSE_TOOL_API fail-closed: only an explicit
// truthy value registers the raw tool endpoint.
func exposeToolAPI(getenv func(string) string) bool {
	return envBool(getenv, "DATAAPI_EXPOSE_TOOL_API")
}

// exposeMCP parses DATAAPI_EXPOSE_MCP the same way: only an explicit truthy value
// mounts the MCP endpoint.
func exposeMCP(getenv func(string) string) bool { return envBool(getenv, "DATAAPI_EXPOSE_MCP") }

// sdeOnly parses DATAAPI_SDE_ONLY: only an explicit truthy value drops the fit corpus.
func sdeOnly(getenv func(string) string) bool { return envBool(getenv, "DATAAPI_SDE_ONLY") }

func envBool(getenv func(string) string, key string) bool {
	b, err := strconv.ParseBool(getenv(key))
	return err == nil && b
}

// exposure says which optional, tool-running endpoints this process serves, and who may
// use the keyed tools on them (nil auth: nobody).
type exposure struct {
	toolAPI, mcp bool
	auth         dataapi.Authenticator
	// publicURL is the validated DATAAPI_PUBLIC_URL ("" means relative links).
	publicURL string
	// upstream overrides the per-caller budget of the live-ESI tools (nil: the default).
	upstream *dataapi.Rate
	// logSalt is DATAAPI_LOG_SALT, the secret of the per-UTC-day caller token ("" means a random
	// secret per process). Never logged.
	logSalt string
	// anthropicRanges is DATAAPI_ANTHROPIC_RANGES: the networks whose access lines carry
	// range="anthropic" (none: no label).
	anthropicRanges []netip.Prefix
	// store is the self-serve key store (nil: none). Its keys authenticate next to auth, and
	// it enforces the daily quota and records usage.
	store *keys.Store
	// keyedFactor is DATAAPI_KEYED_LIMIT_FACTOR (0: the dataapi default).
	keyedFactor int
	// dailyCap is the cap new keys get; it only feeds the generated documents.
	dailyCap int
	// anonDailyCap is DATAAPI_ANON_DAILY_CAP: the requests per UTC day of a client without a
	// key (0: no cap). It applies only when some key source is configured.
	anonDailyCap int
	// maxInFlight is DATAAPI_MAX_INFLIGHT: requests each capped route runs at once (0 = no cap).
	maxInFlight int
	// maxInFlightUpstream is DATAAPI_MAX_INFLIGHT_UPSTREAM: the upstream pool of the routes that
	// can wait on a third party (0 = no split).
	maxInFlightUpstream int
	// sdeBuild yields the SDE build id /v1/health reports at request time ("" when unknown).
	sdeBuild func() string
}

// publicURL resolves DATAAPI_PUBLIC_URL: unset is fine, a value that is not an https origin
// is an error.
func publicURL(getenv func(string) string) (string, error) {
	u, err := dataapi.ValidatePublicURL(getenv("DATAAPI_PUBLIC_URL"))
	if err != nil {
		return "", fmt.Errorf("DATAAPI_PUBLIC_URL: %w", err)
	}
	return u, nil
}

// loadAuth builds the authenticator from DATAAPI_API_KEYS_FILE; unset is no keys (a nil
// interface, not a nil *StaticKeys inside one). The file holds hashes only.
func loadAuth(getenv func(string) string) (dataapi.Authenticator, error) {
	path := getenv("DATAAPI_API_KEYS_FILE")
	if path == "" {
		return nil, nil
	}
	keys, err := dataapi.LoadKeyFile(path)
	if err != nil {
		return nil, fmt.Errorf("DATAAPI_API_KEYS_FILE: %w", err)
	}
	return keys, nil
}

// newHandler builds the API over the deterministic layer. The tool API and the MCP
// server are only ever built in their tiered public form here: this binary is
// internet-facing. The description routes (OpenAPI, llms.txt) are always on; they
// describe whatever else is.
func newHandler(d *bootstrap.Deps, ex exposure, log *slog.Logger) (http.Handler, error) {
	// Untyped nil for an absent side: a nil pointer inside the interface would not be skipped.
	var fileKeys, storeKeys dataapi.Authenticator
	fileKeys = ex.auth
	if ex.store != nil {
		storeKeys = ex.store
	}
	auth := dataapi.AnyOf(fileKeys, storeKeys)
	limits := dataapi.DefaultLimits()
	if ex.upstream != nil {
		limits.UpstreamTool = *ex.upstream
	}
	if auth == nil {
		// Nobody can get a key here: everyone gets the keyed budgets, and no daily cap.
		limits = limits.WithoutKeys()
	}
	// One budget for the tools that read live ESI data, shared by REST and MCP so using
	// both does not double a caller's allowance.
	upstream := dataapi.NewUpstreamLimiter(limits.UpstreamTool, nil)
	cfg := dataapi.Config{
		Deps:                dataapi.NewDeps(d.Tools, d.Retriever),
		Docs:                true,
		Auth:                auth,
		AnonDailyCap:        ex.anonDailyCap,
		PublicURL:           ex.publicURL,
		Limits:              &limits,
		Upstream:            upstream,
		LogSalt:             ex.logSalt,
		AnthropicRanges:     ex.anthropicRanges,
		Logger:              log,
		KeyedLimitFactor:    ex.keyedFactor,
		MaxInFlight:         ex.maxInFlight,
		MaxInFlightUpstream: ex.maxInFlightUpstream,
		SDEBuildFunc:        ex.sdeBuild,
	}
	if ex.store != nil {
		cfg.SelfServe = true
		cfg.Quota = ex.store
		cfg.Usage = ex.store
		// The documents promise a daily cap only when something enforces it.
		cfg.KeyDailyCap = ex.dailyCap
	}
	if ex.toolAPI {
		cfg.ToolAPI = dataapi.ToolAPIPublic
	}
	if ex.mcp {
		// The hosted process sits behind a tunnel that connects over loopback with the
		// public Host header: the SDK's DNS-rebinding guard would refuse every request.
		// dataapi authenticates before the handler runs; the handler only asks whether the
		// caller may use the keyed tier.
		opts := mcpserver.HTTPOptions{
			DisableLocalhostProtection: true,
			Keyed:                      dataapi.KeyedTier,
			Upstream:                   dataapi.UpstreamHook(upstream),
			Logger:                     log,
		}
		if auth != nil {
			opts.AnonDailyCap = ex.anonDailyCap
		}
		if ex.store != nil {
			store := ex.store
			if ex.publicURL != "" {
				opts.KeyHint = ex.publicURL + "/docs/access#access-keys"
			}
			opts.ToolCalled = func(r *http.Request, tool string) {
				if c := dataapi.CallerFrom(r.Context()); c.Authenticated() {
					store.RecordTool(c.ID, tool)
				}
			}
		}
		cfg.MCP = mcpserver.HTTPHandler(d.Tools, opts)
	}
	api, err := dataapi.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("build data api: %w", err)
	}
	return api, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("dataapi failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// The SDE is this service's reason to exist, so failing to open it is fatal. A
	// Qdrant outage at startup is not: the retriever is built anyway and fit search
	// answers 502 per request until Qdrant is back. DATAAPI_SDE_ONLY builds no retriever
	// at all, so fit search is a clean 503.
	cfg := coreconfig.Load()
	onlySDE := sdeOnly(os.Getenv)
	guard, err := esiGuardPercent(os.Getenv)
	if err != nil {
		return err
	}
	upstream, err := upstreamToolRate(os.Getenv)
	if err != nil {
		return err
	}
	deps, err := bootstrap.BuildDepsWith(context.Background(), cfg, bootstrap.Options{
		NoRetriever: onlySDE,
		Client:      newToolsClient(log, guard),
	})
	if err != nil {
		return fmt.Errorf("core deps (check EVE_CORE_SDE_PATH): %w", err)
	}
	defer deps.Close()
	log.Info("core deps ready", "sde", cfg.SDEPath, "corpus", !onlySDE)
	if onlySDE {
		log.Warn("SDE-only mode: fit search is unavailable (503)", "env", "DATAAPI_SDE_ONLY")
	}

	auth, err := loadAuth(os.Getenv)
	if err != nil {
		return err
	}
	pub, err := publicURL(os.Getenv)
	if err != nil {
		return err
	}
	factor, err := keyedFactor(os.Getenv)
	if err != nil {
		return err
	}
	dayCap, err := dailyCap(os.Getenv)
	if err != nil {
		return err
	}
	anonCap, err := anonDailyCap(os.Getenv)
	if err != nil {
		return err
	}
	maxIF, err := maxInFlight(os.Getenv)
	if err != nil {
		return err
	}
	maxIFUp, err := maxInFlightUpstream(os.Getenv)
	if err != nil {
		return err
	}
	ranges, err := anthropicRanges(os.LookupEnv)
	if err != nil {
		return err
	}
	if len(ranges) > 0 {
		log.Info("access log: lines from Anthropic's connector addresses carry range=anthropic (never the address)",
			"env", "DATAAPI_ANTHROPIC_RANGES", "prefixes", len(ranges))
	} else {
		log.Info("access log: no range label, DATAAPI_ANTHROPIC_RANGES is blank", "env", "DATAAPI_ANTHROPIC_RANGES")
	}
	var store *keys.Store
	if path := os.Getenv("DATAAPI_KEYS_DB"); path != "" {
		store, err = keys.Open(path, keys.Options{DailyCap: dayCap, Logger: log})
		if err != nil {
			return fmt.Errorf("DATAAPI_KEYS_DB: %w", err)
		}
		// Runs last (after both servers stop), flushing the usage counters.
		defer func() {
			if err := store.Close(); err != nil {
				log.Error("close key store", "error", err)
			}
		}()
		log.Info("self-serve key store open", "env", "DATAAPI_KEYS_DB", "daily_cap", dayCap)
	}
	ex := exposure{
		toolAPI: exposeToolAPI(os.Getenv), mcp: exposeMCP(os.Getenv), auth: auth, publicURL: pub,
		upstream: &upstream, logSalt: logSalt(os.Getenv, log), anthropicRanges: ranges,
		store: store, keyedFactor: factor, dailyCap: dayCap, anonDailyCap: anonCap, maxInFlight: maxIF, maxInFlightUpstream: maxIFUp, sdeBuild: followSDEBuild(cfg.SDEPath, deps.Tools.SDE, log),
	}
	log.Info("ESI pass-through tools: shared Expires cache, fail-fast budget guard",
		"upstream_tool_limit_per_min", upstream.Max, "upstream_tool_limit_per_min_keyed", upstream.KeyedMax, "esi_guard_percent", guard)
	if auth != nil || store != nil {
		log.Info("callers without an API key get trial limits", "anon_daily_cap", anonCap, "env", "DATAAPI_ANON_DAILY_CAP")
	}
	if auth != nil {
		log.Info("API keys loaded: the keyed tool tier is available", "env", "DATAAPI_API_KEYS_FILE")
	}
	if ex.toolAPI {
		log.Warn("tool API exposed: POST /v1/tool/{name} is registered (per-caller limit, tool tiers enforced)",
			"env", "DATAAPI_EXPOSE_TOOL_API")
	}
	if ex.mcp {
		log.Warn("MCP exposed: POST /v1/mcp is mounted (per-caller limit, tool set chosen per request by tier)",
			"env", "DATAAPI_EXPOSE_MCP")
	}
	handler, err := newHandler(deps, ex, log)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              listenAddr(os.Getenv),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Above dataapi.DefaultToolTimeout: a tool is cut off by its own deadline
		// before the connection is.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := checkAdminToken(os.Getenv("DATAAPI_ADMIN_TOKEN")); err != nil {
		return err
	}
	servers := []*http.Server{srv}
	if adm := adminServer(store, os.Getenv("DATAAPI_ADMIN_TOKEN"), adminAddr(os.Getenv), log); adm != nil {
		servers = append(servers, adm)
	} else if os.Getenv("DATAAPI_ADMIN_TOKEN") != "" {
		log.Warn("admin token set but no key store: the admin API is not served", "env", "DATAAPI_KEYS_DB")
	}

	errc := make(chan error, len(servers))
	for i, s := range servers {
		go func() {
			if i == 0 {
				log.Info("dataapi listening", "addr", s.Addr)
			} else {
				log.Info("admin API listening", "addr", s.Addr)
			}
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	var serveErr error
	select {
	case serveErr = <-errc:
		log.Error("server failed; stopping the other listener", "error", serveErr)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}
	// Stop accepting on every server (in parallel, sharing one deadline) before the deferred
	// store.Close flushes the usage counters.
	if err := shutdownAll(servers, shutdownBudget); err != nil {
		if serveErr == nil {
			return err
		}
		log.Error("graceful shutdown", "error", err)
	}
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	log.Info("dataapi stopped")
	return nil
}

// shutdownAll gracefully shuts the servers down concurrently within one timeout and returns
// the first error.
func shutdownAll(servers []*http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Shutdown(ctx); err != nil {
				errs[i] = fmt.Errorf("graceful shutdown %s: %w", s.Addr, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
