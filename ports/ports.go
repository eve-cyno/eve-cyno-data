// Package ports holds the embedding contract shared by the corpus (rag) and the
// ingest pipeline. It is Product A code and a leaf — it imports nothing in-repo.
// The streaming chat contract (Chat, ChatMessage, ChatChunk) is Product B and
// lives in chat/brain/ports.
package ports

import "context"

// Embedder is the embedding backend (ollama | deepinfra | openai-compatible) that turns
// a text into its vector.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}
