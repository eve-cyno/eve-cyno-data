package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaults(t *testing.T) {
	for _, k := range []string{"EVE_CORE_SDE_PATH", "EVE_CORE_QDRANT_URL", "EVE_CORE_OLLAMA_URL", "EVE_CORE_EMBED_MODEL", "JANICE_API_KEY"} {
		t.Setenv(k, "")
	}
	c := Load()
	require.Equal(t, "http://localhost:6333", c.QdrantURL)
	require.Equal(t, "http://localhost:11434", c.OllamaURL)
	require.Equal(t, "nomic-embed-text", c.EmbedModel)
	require.NotEmpty(t, c.SDEPath, "SDEPath should resolve to a non-empty path")
	require.Empty(t, c.JaniceAPIKey)
}

// TestSDEReloadInterval covers EVE_CORE_SDE_RELOAD_INTERVAL: unset keeps the
// default (0 = "use the sde package default"), a duration overrides it, and
// 0/off disables hot-reload (-1). Garbage falls back to the default.
func TestSDEReloadInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":         0,
		"10m":      10 * time.Minute,
		"90s":      90 * time.Second,
		"0":        -1,
		"off":      -1,
		"disabled": -1,
		"false":    -1,
		"nonsense": 0,
		"-5m":      0,
		"0s":       -1,
		"  2m  ":   2 * time.Minute,
	}
	for in, want := range cases {
		t.Run("env="+in, func(t *testing.T) {
			t.Setenv("EVE_CORE_SDE_RELOAD_INTERVAL", in)
			require.Equal(t, want, Load().SDEReloadInterval)
		})
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("EVE_CORE_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("EVE_CORE_OLLAMA_URL", "http://ollama:11434")
	t.Setenv("EVE_CORE_SDE_PATH", "/custom/sde.sqlite")
	t.Setenv("JANICE_API_KEY", "k123")
	c := Load()
	require.Equal(t, "http://qdrant:6333", c.QdrantURL)
	require.Equal(t, "http://ollama:11434", c.OllamaURL)
	require.Equal(t, "/custom/sde.sqlite", c.SDEPath)
	require.Equal(t, "k123", c.JaniceAPIKey)
}

// TestEmbedQueryInstruction covers EMBED_QUERY_INSTRUCTION: unset/blank keeps the
// default (empty = query embeddings are sent raw, today's behaviour); a value is
// passed through with surrounding whitespace trimmed.
func TestEmbedQueryInstruction(t *testing.T) {
	t.Setenv("EMBED_QUERY_INSTRUCTION", "")
	require.Empty(t, Load().EmbedQueryInstruction)

	t.Setenv("EMBED_QUERY_INSTRUCTION", "   ")
	require.Empty(t, Load().EmbedQueryInstruction)

	t.Setenv("EMBED_QUERY_INSTRUCTION", "  Given a web search query, retrieve relevant passages that answer the query \n")
	require.Equal(t, "Given a web search query, retrieve relevant passages that answer the query", Load().EmbedQueryInstruction)
}
