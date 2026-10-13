// Command dataapi serves Product A's HTTP data API (core/dataapi) on its own:
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
// The tool endpoint and the MCP endpoint apply the same tool tiers (core/catalog): public
// tools for anyone (the SDE lookups and the ESI pass-throughs, the latter with a lower
// per-caller limit), keyed tools (get_fits, list_fits, analyze_battle)
// only with a valid API key, appraise_items only with the caller's own Janice key in the
// X-Janice-Key header (never the project's), and disabled tools (convert_isk_to_real) never.
// An API key is sent as "Authorization: Bearer <key>" or "X-API-Key: <key>". Each document
// describes exactly the endpoints this process serves.
//
// Configuration comes from the process environment (it does not read a .env
// file; for a local run: `set -a; . ./.env; set +a`):
//
//	DATAAPI_ADDR              listen address (default :8092)
//	DATAAPI_EXPOSE_TOOL_API   register POST /v1/tool/{name} (default false; when on,
//	                          30 req/min per caller, tool tiers enforced)
//	DATAAPI_EXPOSE_MCP        mount POST /v1/mcp (default false; 60 req/min/IP)
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
//	DATAAPI_UPSTREAM_TOOL_LIMIT  calls per minute per caller of the tools that read live ESI
//	                          data (get_market_price, get_type_info, get_sovereignty,
//	                          get_system_activity), REST and MCP together (default 10; 0 = no
//	                          extra limit)
//	DATAAPI_ESI_GUARD_PERCENT share (0-90, default 25) of every ESI rate-limit group's tokens
//	                          kept unspent; below it a tool answers 503 upstream_busy instead
//	                          of calling ESI. Every ESI call is logged as msg "esi_call"
//	DATAAPI_LOG_SALT          salt of the opaque "caller" token in the tool/MCP access-log
//	                          lines (default: random per process)
//	ESI_CONTACT               contact for the ESI User-Agent (core/esi)
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
//	DATAAPI_KEYED_LIMIT_FACTOR  multiple of the route limits a keyed caller gets (integer 1-10,
//	                          default 2); the live-ESI tool budget is never multiplied
//	DATAAPI_ADMIN_TOKEN       bearer token of the admin API that issues, revokes and audits
//	                          keys. Unset: no admin listener. Never logged
//	DATAAPI_ADMIN_ADDR        admin listen address (default 127.0.0.1:8093; the container
//	                          setup sets :8093 so the internal network reaches it); served only
//	                          when both the key store and the admin token are set. The token must
//	                          be at least 32 characters, else startup fails
//	EVE_CORE_*, QDRANT_*, DEEPINFRA_EMBED_*   the deterministic layer (core/config)
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
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/catalog"
	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/dataapi/keys"
	"eve-cyno.dev/go/data/mcpserver"
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
	// logSalt is DATAAPI_LOG_SALT ("" means a random salt per process).
	logSalt string
	// store is the self-serve key store (nil: none). Its keys authenticate next to auth, and
	// it enforces the daily quota and records usage.
	store *keys.Store
	// keyedFactor is DATAAPI_KEYED_LIMIT_FACTOR (0: the dataapi default).
	keyedFactor int
	// dailyCap is the cap new keys get; it only feeds the generated documents.
	dailyCap int
	// sdeBuild is the SDE build id /v1/health reports ("" when unknown).
	sdeBuild string
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
	limits := dataapi.DefaultLimits()
	if ex.upstream != nil {
		limits.UpstreamTool = *ex.upstream
	}
	// One budget for the tools that read live ESI data, shared by REST and MCP so using
	// both does not double a caller's allowance.
	upstream := dataapi.NewUpstreamLimiter(limits.UpstreamTool, nil)
	// Untyped nil for an absent side: a nil pointer inside the interface would not be skipped.
	var fileKeys, storeKeys dataapi.Authenticator
	fileKeys = ex.auth
	if ex.store != nil {
		storeKeys = ex.store
	}
	cfg := dataapi.Config{
		Deps:             dataapi.NewDeps(d.Tools, d.Retriever),
		Docs:             true,
		Auth:             dataapi.AnyOf(fileKeys, storeKeys),
		PublicURL:        ex.publicURL,
		Limits:           &limits,
		Upstream:         upstream,
		LogSalt:          ex.logSalt,
		Logger:           log,
		KeyedLimitFactor: ex.keyedFactor,
		SDEBuild:         ex.sdeBuild,
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
			Keyed:                      func(r *http.Request) bool { return dataapi.CallerFrom(r.Context()).Allows(catalog.TierKeyed) },
			Upstream:                   upstream.Allow,
			Logger:                     log,
		}
		if ex.store != nil {
			store := ex.store
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
		upstream: &upstream, logSalt: os.Getenv("DATAAPI_LOG_SALT"),
		store: store, keyedFactor: factor, dailyCap: dayCap, sdeBuild: sdeBuild(cfg.SDEPath, log),
	}
	log.Info("ESI pass-through tools: shared Expires cache, fail-fast budget guard",
		"upstream_tool_limit_per_min", upstream.Max, "esi_guard_percent", guard)
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
