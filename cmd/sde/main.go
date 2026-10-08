// Command sde builds (or refreshes) the SDE SQLite file that every other core binary
// reads, from the Fuzzwork dump of CCP's Static Data Export. It is the standalone
// counterpart of `ingest sde` (ingest/cmd/ingest) and runs the same builder
// (core/sde/build), so a checkout of the core module alone can make its own SDE.
//
//	go run ./cmd/sde                   # from the core module; writes EVE_CORE_SDE_PATH
//	go run ./cmd/sde -out data/sde/sde.sqlite
//	go run ./cmd/sde -force            # rebuild even when the build id is unchanged
//
// The destination is -out, else EVE_CORE_SDE_PATH, else data/sde/sde.sqlite (see
// core/config). The build needs internet access to fuzzwork.co.uk (about 440 MB on
// disk, a few minutes at worst) and identifies itself to Fuzzwork with a User-Agent.
// The new file replaces the old one atomically only after it passes the publish gate;
// on any failure the existing file is untouched and the exit status is 1. Progress is
// logged to stderr.
//
// The build id of the last successful run is kept in "<out>.build" next to the file,
// so a second run is a cheap no-op until Fuzzwork publishes a new dump (after a game
// patch). This file is this command's own state; `ingest sde` keeps its own in
// ingest.db and the two do not read each other's.
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
	"path/filepath"
	"strings"
	"syscall"

	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/sde/build"
)

// refresher is the part of *build.Builder this command uses; tests inject a fake.
type refresher interface {
	Refresh(ctx context.Context, recorded string) (latest string, rebuilt bool, err error)
}

// options is the parsed command line.
type options struct {
	out   string
	force bool
}

// parseArgs resolves the destination: -out, then EVE_CORE_SDE_PATH, then the
// core/config default.
func parseArgs(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("sde", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "path of the SDE SQLite file (default: $EVE_CORE_SDE_PATH, else data/sde/sde.sqlite)")
	force := fs.Bool("force", false, "rebuild even when the build id is unchanged")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	path := *out
	if path == "" {
		path = getenv("EVE_CORE_SDE_PATH")
	}
	if path == "" {
		path = coreconfig.Load().SDEPath
	}
	return options{out: path, force: *force}, nil
}

// buildIDPath is where the last successful build id is recorded.
func buildIDPath(out string) string { return out + ".build" }

func readBuildID(out string) string {
	b, err := os.ReadFile(buildIDPath(out))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// run refreshes opts.out with r and records the build id on success.
func run(ctx context.Context, r refresher, opts options, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(opts.out), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	recorded := ""
	if !opts.force {
		recorded = readBuildID(opts.out)
	}
	log.Info("sde: refreshing", "path", opts.out, "recorded_build", recorded, "force", opts.force)
	latest, rebuilt, err := r.Refresh(ctx, recorded)
	if err != nil {
		return fmt.Errorf("sde refresh: %w", err)
	}
	if err := os.WriteFile(buildIDPath(opts.out), []byte(latest+"\n"), 0o644); err != nil {
		return fmt.Errorf("record build id: %w", err)
	}
	if rebuilt {
		log.Info("sde: built", "path", opts.out, "build", latest)
	} else {
		log.Info("sde: already current", "path", opts.out, "build", latest)
	}
	return nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log) // the builder logs through the default logger

	opts, err := parseArgs(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sde:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, build.New(opts.out), opts, log); err != nil {
		log.Error("sde failed", "error", err)
		os.Exit(1)
	}
}
