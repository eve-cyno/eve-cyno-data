// Command mcp serves the EVE-Cyno tools to a local MCP client (Claude Desktop, Claude
// Code, any stdio MCP host) over stdin/stdout: SDE lookups, fit validation and stats,
// market prices, sovereignty and system activity, computed from the local SDE and ESI.
//
//	go run ./cmd/mcp          # from the core module; or build it: go build -o eve-cyno-mcp ./cmd/mcp
//
// Stdout is the protocol channel and carries nothing else; logs go to stderr as JSON.
//
// By default only the public tier is registered (the tools the hosted /v1/mcp endpoint
// serves to anyone). Every other tool (the keyed ones: get_fits, list_fits, the ESI
// pass-throughs, analyze_battle; appraise_items, which spends a Janice key; and
// convert_isk_to_real, disabled on public surfaces) is registered only with -all or
// MCP_EXPOSE_ALL=true, for a process that runs on its owner's machine with the owner's
// keys from the environment (JANICE_API_KEY, QDRANT_*, ...). The X-Janice-Key header
// belongs to the HTTP endpoint; over stdio the key comes from the environment.
//
// Configuration comes from the process environment (it does not read a .env file):
//
//	EVE_CORE_SDE_PATH   the SDE SQLite (default: data/sde/sde.sqlite found upward from the
//	                    working directory); the server cannot start without it
//	MCP_EXPOSE_ALL      register every tool (default false; the -all flag does the same)
//	MCP_SDE_ONLY        true runs without the community-fit corpus (default false; the
//	                    -sde-only flag does the same): no Qdrant client is built, Qdrant is
//	                    never contacted, and get_fits / list_fits say the corpus is not
//	                    configured. Explicit only: it is never inferred from a missing QDRANT_URL
//	EVE_CORE_*, QDRANT_*, DEEPINFRA_EMBED_*, JANICE_API_KEY
//	                    the rest of the deterministic layer (core/config); needed only by
//	                    the tools that use them (-all for fit search and appraisal)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"eve-cyno.dev/go/data/bootstrap"
	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/mcpserver"
	"eve-cyno.dev/go/data/tools"
)

// exposeAll resolves -all / MCP_EXPOSE_ALL fail-closed: only an explicit truthy value
// registers every tool beyond the public tier.
func exposeAll(flagAll bool, getenv func(string) string) bool {
	if flagAll {
		return true
	}
	b, err := strconv.ParseBool(getenv("MCP_EXPOSE_ALL"))
	return err == nil && b
}

// sdeOnly resolves -sde-only / MCP_SDE_ONLY with the same fail-closed parsing as
// exposeAll (and DATAAPI_SDE_ONLY): only an explicit truthy value drops the corpus.
func sdeOnly(flagOnly bool, getenv func(string) string) bool {
	if flagOnly {
		return true
	}
	b, err := strconv.ParseBool(getenv("MCP_SDE_ONLY"))
	return err == nil && b
}

// serve runs one MCP session over t until the client disconnects or ctx ends. A client
// closing the pipe is the normal way a stdio server ends, so it is not an error.
func serve(ctx context.Context, deps *tools.Deps, opts mcpserver.Options, t mcp.Transport) error {
	err := mcpserver.New(deps, opts).Run(ctx, t)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	all := flag.Bool("all", false, "register every tool, whatever its public tier (keyed, byo-key, disabled); they use this machine's keys; local use only")
	onlySDE := flag.Bool("sde-only", false, "run without the community-fit corpus: Qdrant is never contacted and get_fits / list_fits say the corpus is not configured (same as MCP_SDE_ONLY=true)")
	flag.Parse()

	if err := run(log, exposeAll(*all, os.Getenv), sdeOnly(*onlySDE, os.Getenv)); err != nil {
		log.Error("mcp failed", "error", err)
		os.Exit(1)
	}
}

// buildDeps opens the deterministic layer; onlySDE builds no corpus retriever at all, so
// Qdrant is never contacted and the fit tools say the corpus is not configured.
func buildDeps(ctx context.Context, cfg coreconfig.CoreConfig, onlySDE bool) (*bootstrap.Deps, error) {
	return bootstrap.BuildDepsWith(ctx, cfg, bootstrap.Options{NoRetriever: onlySDE})
}

func run(log *slog.Logger, all, onlySDE bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := coreconfig.Load()
	deps, err := buildDeps(ctx, cfg, onlySDE)
	if err != nil {
		return fmt.Errorf("core deps (check EVE_CORE_SDE_PATH): %w", err)
	}
	defer deps.Close()

	opts := mcpserver.Options{All: all, Logger: log}
	log.Info("mcp server ready", "sde", cfg.SDEPath, "tools", len(mcpserver.Exposed(opts)), "all", all, "corpus", !onlySDE)
	if onlySDE {
		log.Warn("SDE-only mode: get_fits and list_fits report the corpus as not configured", "env", "MCP_SDE_ONLY")
	}
	if all {
		log.Warn("every tool registered, including keyed, byo-key and disabled ones: they use this machine's keys")
	}
	return serve(ctx, deps.Tools, opts, &mcp.StdioTransport{})
}
