// Package bootstrap builds the in-process deterministic-layer dependencies
// (SDE handle, HTTP tool client, Qdrant retriever) from a CoreConfig. Every Go
// face calls BuildDeps once at startup and shares the result.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/ports"
	"eve-cyno.dev/go/data/rag"
	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/tools"
)

// defaultTopK mirrors Python QdrantRetriever default (similarity_top_k = 5).
const defaultTopK = 5

// Deps bundles the tool executor and RAG retriever for in-process tool calls.
type Deps struct {
	Tools     *tools.Deps
	Retriever *rag.QdrantRetriever
	close     func() error
}

// Close releases the SDE database handle.
func (d *Deps) Close() error { return d.close() }

// sdeReloadInterval resolves CoreConfig.SDEReloadInterval: unset (0) means the
// sde package default, so a long-running process follows an SDE file that the
// ingest replaces by atomic rename; a negative value disables hot-reload.
func sdeReloadInterval(cfg config.CoreConfig) time.Duration {
	if cfg.SDEReloadInterval == 0 {
		return sde.DefaultReloadInterval
	}
	return cfg.SDEReloadInterval
}

// queryEmbedClient builds the embed client the retriever uses for QUERY
// embeddings (Retrieve and the fit-search vector pass): the OpenAI-compatible
// endpoint when EmbedBaseURL is set, else local Ollama. When
// cfg.EmbedQueryInstruction is set the client is wrapped so each query is sent as
// "Instruct: <instruction>\nQuery: <query>" (Qwen3-Embedding); empty leaves it
// untouched. Documents are embedded by ingest with its own, undecorated
// client, so they never get the prefix.
func queryEmbedClient(cfg config.CoreConfig) ports.Embedder {
	var embed ports.Embedder
	if cfg.EmbedBaseURL != "" {
		model := cfg.EmbedModelName
		if model == "" {
			model = "BAAI/bge-large-en-v1.5"
		}
		embed = rag.NewOpenAIEmbedClient(cfg.EmbedBaseURL, model, cfg.EmbedAPIKey)
	} else {
		embed = rag.NewOllamaEmbedClient(cfg.OllamaURL, cfg.EmbedModel)
	}
	return rag.WithQueryInstruction(embed, cfg.EmbedQueryInstruction)
}

// BuildDeps opens the SDE, builds the tool HTTP client and Qdrant retriever.
// LoadTitles is called best-effort — a Qdrant outage at startup is non-fatal: the
// retriever is still built (it is lazy), SDE tools keep working, and each fit-search
// request fails on its own (dataapi: 502 upstream_error) until Qdrant is back. For a
// deployment that has no Qdrant at all use Options.NoRetriever. The SDE handle
// hot-reloads when the file is replaced (see sde.OpenWithReload).
func BuildDeps(ctx context.Context, cfg config.CoreConfig) (*Deps, error) {
	return BuildDepsWith(ctx, cfg, Options{})
}

// Options tunes BuildDepsWith. The zero value is exactly BuildDeps, so production
// callers keep using BuildDeps; the knobs exist for offline test fixtures.
type Options struct {
	// SkipRetrieverTitles skips the startup LoadTitles call, so building the deps
	// makes no Qdrant request. The retriever is still constructed (it is lazy: any
	// later search just fails or returns nothing when Qdrant is down).
	SkipRetrieverTitles bool
	// Client replaces the real tools HTTP/ESI client (tools.NewClient) when non-nil.
	Client *tools.Client
	// NoRetriever is SDE-only mode: no retriever is built (Deps.Retriever and
	// Tools.Retriever stay nil) and Qdrant is never contacted. The fit-search routes of
	// dataapi then answer 503 `unavailable`, /health reports corpus:false and
	// get_fits / list_fits say the corpus is not configured. SkipRetrieverTitles is moot.
	NoRetriever bool
}

// BuildDepsWith is BuildDeps with Options.
func BuildDepsWith(ctx context.Context, cfg config.CoreConfig, opts Options) (*Deps, error) {
	s, err := sde.OpenWithReload(cfg.SDEPath, sdeReloadInterval(cfg))
	if err != nil {
		return nil, fmt.Errorf("open sde %q: %w", cfg.SDEPath, err)
	}
	// The fit search demotes fits whose main weapon system the hull has no bonus
	// for; the hull's bonuses come from the SDE (invTraits).
	var retr *rag.QdrantRetriever // stays a nil pointer in SDE-only mode
	if !opts.NoRetriever {
		retr = rag.NewQdrantRetriever(cfg.QdrantURL, cfg.QdrantCollection, queryEmbedClient(cfg), defaultTopK).
			WithWeaponAffinity(hullWeaponAffinity(s))
		// LoadTitles is best-effort with a tight timeout so a hung Qdrant doesn't stall startup.
		if !opts.SkipRetrieverTitles {
			titlesCtx, titlesCancel := context.WithTimeout(ctx, 8*time.Second)
			_ = retr.LoadTitles(titlesCtx)
			titlesCancel()
		}
	}
	client := opts.Client
	if client == nil {
		client = tools.NewClient()
	}

	// One dogma engine for the process: compute_fit_stats, the validate_fitting stat
	// card and /fit/stats (dataapi.NewDeps) share its warm SDE memo.
	td := &tools.Deps{SDE: s, Client: client, Retriever: retr, JaniceAPIKey: cfg.JaniceAPIKey, Stats: gofa.New(s)}
	// One Alpha legality checker for the process: validate_fitting and the fit
	// controller share it. A failed allowlist load is non-fatal (nil → the tool falls
	// notes the skipped Alpha check, the controller stays off).
	if al, err := fit.LoadAlphaAllowlist(s); err != nil {
		slog.Warn("bootstrap: alpha legality unavailable", "error", err)
	} else {
		td.Legality = fit.NewLegality(s, al)
	}
	return &Deps{Tools: td, Retriever: retr, close: s.Close}, nil
}
