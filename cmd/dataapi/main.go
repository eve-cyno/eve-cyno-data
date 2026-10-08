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
//
// The tool endpoint and the MCP endpoint apply the same tool tiers (core/catalog): public
// tools for anyone, keyed tools (get_fits, list_fits, the ESI pass-throughs, analyze_battle)
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
//	                          # comments allowed. Unset means no keys: the keyed tier is
//	                          unavailable. A keyed caller is rate limited per key id.
//	DATAAPI_SDE_ONLY          true runs without the community-fit corpus: no Qdrant client is
//	                          built or contacted, fit search answers 503 "unavailable",
//	                          /v1/health reports corpus:false and get_fits / list_fits say the
//	                          corpus is not configured. Unset (default): Qdrant is expected; if
//	                          it is configured but down, fit search answers 502 "upstream_error"
//	                          per request and /v1/health still reports corpus:true
//	EVE_CORE_*, QDRANT_*, DEEPINFRA_EMBED_*   the deterministic layer (core/config)
//
// Client IPs for the limiters come from Cloudflare's CF-Connecting-IP header and
// fall back to the TCP peer, so the service must only be reachable through the
// Cloudflare tunnel or on loopback. For the same reason the MCP transport's
// DNS-rebinding guard (it refuses a loopback connection that carries a foreign Host
// header, which is exactly what the tunnel's connector sends) is switched off.
// It is not deployed yet: no compose service, tunnel route or release job exists
// for it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"eve-cyno.dev/go/data/bootstrap"
	"eve-cyno.dev/go/data/catalog"
	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/mcpserver"
)

const defaultAddr = ":8092"

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
	cfg := dataapi.Config{
		Deps:   dataapi.NewDeps(d.Tools, d.Retriever),
		Docs:   true,
		Auth:   ex.auth,
		Logger: log,
	}
	if ex.toolAPI {
		cfg.ToolAPI = dataapi.ToolAPIPublic
	}
	if ex.mcp {
		// The hosted process sits behind a tunnel that connects over loopback with the
		// public Host header: the SDK's DNS-rebinding guard would refuse every request.
		// dataapi authenticates before the handler runs; the handler only asks whether the
		// caller may use the keyed tier.
		cfg.MCP = mcpserver.HTTPHandler(d.Tools, mcpserver.HTTPOptions{
			DisableLocalhostProtection: true,
			Keyed:                      func(r *http.Request) bool { return dataapi.CallerFrom(r.Context()).Allows(catalog.TierKeyed) },
			Logger:                     log,
		})
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
	deps, err := bootstrap.BuildDepsWith(context.Background(), cfg, bootstrap.Options{NoRetriever: onlySDE})
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
	ex := exposure{toolAPI: exposeToolAPI(os.Getenv), mcp: exposeMCP(os.Getenv), auth: auth}
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

	errc := make(chan error, 1)
	go func() {
		log.Info("dataapi listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("dataapi stopped")
	return nil
}
