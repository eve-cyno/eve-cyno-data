package rag

import (
	"context"
	"strings"

	"eve-cyno.dev/go/data/ports"
)

// queryInstructionLLM prefixes every embedded text with an instruction, in the
// asymmetric form instruction-tuned embedding models expect on the QUERY side
// only (documents are embedded raw):
//
//	Instruct: <task>\nQuery: <query text>
//
// It is the format Qwen3-Embedding documents; models that need no instruction
// (bge-large as used today) simply run without this wrapper.
type queryInstructionLLM struct {
	inner  ports.Embedder
	prefix string
}

// WithQueryInstruction decorates inner so each Embed call sends
// "Instruct: <instruction>\nQuery: <text>". An empty or whitespace-only
// instruction returns inner unchanged, so the default behaviour is byte-identical.
//
// Apply it only to the retrieval (query) embed client. The ingest path embeds
// documents and must keep using the undecorated client.
func WithQueryInstruction(inner ports.Embedder, instruction string) ports.Embedder {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return inner
	}
	return &queryInstructionLLM{inner: inner, prefix: "Instruct: " + instruction + "\nQuery: "}
}

// Embed implements ports.Embedder.
func (q *queryInstructionLLM) Embed(ctx context.Context, text string) ([]float32, error) {
	return q.inner.Embed(ctx, q.prefix+text)
}
