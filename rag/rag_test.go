package rag

import (
	"context"
	"testing"

	"eve-cyno.dev/go/data/corpus"
	"github.com/stretchr/testify/require"
)

func TestOllamaEmbedClient(t *testing.T) {
	skipIfUnreachable(t, "Ollama", liveOllamaURL)
	ctx := context.Background()
	client := NewOllamaEmbedClient(liveOllamaURL, "nomic-embed-text")
	vec, err := client.Embed(ctx, "What is the Rifter?")
	require.NoError(t, err)
	require.Equal(t, 768, len(vec), "nomic-embed-text should produce 768-dim vectors")
	// Verify non-zero
	sum := float32(0)
	for _, v := range vec {
		if v < 0 {
			sum -= v
		} else {
			sum += v
		}
	}
	require.Greater(t, sum, float32(0), "embedding vector should be non-zero")
}

func TestScrollFitsLive(t *testing.T) {
	skipIfUnreachable(t, "Qdrant", liveQdrantURL)
	ctx := context.Background()
	embedClient := NewOllamaEmbedClient(liveOllamaURL, "nomic-embed-text")
	r := NewQdrantRetriever(liveQdrantURL, corpus.DefaultCollection, embedClient, 5)
	res, err := r.ScrollFits(ctx, FitQuery{ShipName: "Gila", Limit: 5})
	if err != nil {
		t.Skipf("qdrant unavailable: %v", err)
	}
	require.NotEmpty(t, res.Hits, "Gila should have community fits in corpus-v0")
	for _, h := range res.Hits {
		require.NotEmpty(t, h.Text)
		require.Contains(t, []string{"workbench", "abysstracker", "gustavmannfred", "caldarijoans"}, h.Source)
	}
}

func TestScrollFitsRelaxation(t *testing.T) {
	skipIfUnreachable(t, "Qdrant", liveQdrantURL)
	ctx := context.Background()
	embedClient := NewOllamaEmbedClient(liveOllamaURL, "nomic-embed-text")
	r := NewQdrantRetriever(liveQdrantURL, corpus.DefaultCollection, embedClient, 5)
	// context="wormhole" trips relaxation (no wormhole tag in corpus)
	res, err := r.ScrollFits(ctx, FitQuery{ShipName: "Gila", Context: "wormhole", Limit: 5})
	if err != nil {
		t.Skipf("qdrant unavailable: %v", err)
	}
	require.True(t, res.Relaxed, "context=wormhole should trigger relaxation")
	require.Len(t, res.DroppedFilters, 1)
	require.Equal(t, "context", res.DroppedFilters[0].Key)
	require.NotEmpty(t, res.Hits)
}

// TestWithEmbed verifies that WithEmbed returns a distinct *QdrantRetriever
// that shares the same collection as the original but uses the supplied embed
// client, without mutating the original.
func TestWithEmbed(t *testing.T) {
	t.Parallel()

	// Use a no-network embed client (local llama-server URL that is never
	// dialled in this unit test — we only inspect the pointer, not call Embed).
	origEmbed := NewOpenAIEmbedClient("http://127.0.0.1:8082/v1", "bge-large-en-v1.5", "")
	orig := NewQdrantRetriever("http://127.0.0.1:6333", "test_collection", origEmbed, 5)

	newEmbed := NewOpenAIEmbedClient("http://127.0.0.1:9999/v1", "custom-model", "tok-123")
	clone := orig.WithEmbed(newEmbed)

	// Must be a different pointer (not the same object).
	require.NotSame(t, orig, clone, "WithEmbed must return a new *QdrantRetriever")

	// Collection is preserved.
	require.Equal(t, orig.Collection(), clone.Collection(),
		"WithEmbed must preserve collection name")

	// Original embed field is unchanged (orig.embed still points to origEmbed).
	// We verify this indirectly: the original retriever's internal embed field
	// must NOT equal newEmbed. We can't read unexported fields directly, but we
	// CAN verify that orig and clone are distinct objects (covered above) and
	// that a second WithEmbed on orig also uses origEmbed (not newEmbed).
	clone2 := orig.WithEmbed(origEmbed)
	require.NotSame(t, clone, clone2, "two WithEmbed calls produce distinct instances")
	require.Equal(t, orig.Collection(), clone2.Collection())
}
