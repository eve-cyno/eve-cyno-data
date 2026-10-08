// Package config provides the shared runtime configuration for the Go faces
// (discord-bot, native) and cmd/dataapi. It locates the deterministic
// layer's backing stores — SDE SQLite, Qdrant, Ollama (for embeddings) — and
// the Janice API key. Defaults match the standard local dev setup so a dev
// needs no new env vars.
package config

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// CoreConfig holds the deterministic-layer runtime configuration.
type CoreConfig struct {
	SDEPath      string // EVE_CORE_SDE_PATH    (default: data/sde/sde.sqlite from the working dir / repo root, see paths.go)
	QdrantURL    string // EVE_CORE_QDRANT_URL  (default: http://localhost:6333)
	OllamaURL    string // EVE_CORE_OLLAMA_URL  (default: http://localhost:11434)
	EmbedModel   string // EVE_CORE_EMBED_MODEL (default: nomic-embed-text) — Ollama model name
	JaniceAPIKey string // JANICE_API_KEY       (default: "")

	// SDEReloadInterval is how often (at most) a running process checks whether the
	// ingest replaced the SDE file and reopens it: EVE_CORE_SDE_RELOAD_INTERVAL as a
	// Go duration ("5m"). 0 (unset) = the sde package default; < 0 ("0"/"off") =
	// hot-reload disabled.
	SDEReloadInterval time.Duration

	// Embedding backend selection (shared by core-api, native-web, discord-bot
	// and ingest so they all use the same model/collection). When EmbedBaseURL is
	// set, the OpenAI-compatible endpoint is used (local llama.cpp llama-server or
	// cloud DeepInfra) instead of Ollama.
	QdrantCollection string // QDRANT_COLLECTION        (default: eve_knowledge)
	EmbedBaseURL     string // DEEPINFRA_EMBED_BASE_URL  (default: "" → use Ollama)
	EmbedModelName   string // EMBEDDING_MODEL           (default: "" → BAAI/bge-large-en-v1.5 when EmbedBaseURL set)
	EmbedAPIKey      string // DEEPINFRA_API_KEY         (default: "" — empty is fine for local llama.cpp)

	// EmbedQueryInstruction is the task instruction for instruction-tuned embedding
	// models (Qwen3-Embedding): EMBED_QUERY_INSTRUCTION, whitespace-trimmed. When
	// non-empty, every QUERY embedding on the retrieval path is sent as
	// "Instruct: <instruction>\nQuery: <query>"; documents (ingest) are always
	// embedded raw. Empty (default) = queries are sent as-is (bge-large behaviour).
	EmbedQueryInstruction string
}

// Load reads CoreConfig from env, falling back to defaults that match the
// standard local dev setup (core-api on :8000, Qdrant on :6333, Ollama on :11434).
func Load() CoreConfig {
	return CoreConfig{
		SDEPath:      envOr("EVE_CORE_SDE_PATH", defaultSDEPath()),
		QdrantURL:    envOr("EVE_CORE_QDRANT_URL", "http://localhost:6333"),
		OllamaURL:    envOr("EVE_CORE_OLLAMA_URL", "http://localhost:11434"),
		EmbedModel:   envOr("EVE_CORE_EMBED_MODEL", "nomic-embed-text"),
		JaniceAPIKey: os.Getenv("JANICE_API_KEY"),

		SDEReloadInterval: parseReloadInterval(os.Getenv("EVE_CORE_SDE_RELOAD_INTERVAL")),

		QdrantCollection: envOr("QDRANT_COLLECTION", "eve_knowledge"),
		EmbedBaseURL:     os.Getenv("DEEPINFRA_EMBED_BASE_URL"),
		EmbedModelName:   os.Getenv("EMBEDDING_MODEL"),
		EmbedAPIKey:      os.Getenv("DEEPINFRA_API_KEY"),

		EmbedQueryInstruction: strings.TrimSpace(os.Getenv("EMBED_QUERY_INSTRUCTION")),
	}
}

// parseReloadInterval maps EVE_CORE_SDE_RELOAD_INTERVAL to CoreConfig.SDEReloadInterval:
// "" and unparsable/negative values -> 0 (default); "0", "0s", "off", "false",
// "disabled" -> -1 (disabled); a positive Go duration -> that duration.
func parseReloadInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return 0
	case "off", "false", "disabled":
		return -1
	}
	d, err := time.ParseDuration(raw)
	switch {
	case err != nil || d < 0:
		slog.Warn("ignoring invalid EVE_CORE_SDE_RELOAD_INTERVAL; using default", "value", raw)
		return 0
	case d == 0:
		return -1
	default:
		return d
	}
}

func envOr(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}
