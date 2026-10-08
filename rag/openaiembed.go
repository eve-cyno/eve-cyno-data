package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"eve-cyno.dev/go/data/ports"
)

// openAIEmbedClient embeds via an OpenAI-compatible POST {base}/embeddings
// endpoint — a local llama.cpp llama-server (e.g. bge-large on :8082) or cloud
// DeepInfra. Used on the read/retrieval side to match the bge-large-1024 collection
// (eve_knowledge_v3_go_bge). Implements ports.Embedder.
//
// Shrink-and-retry: bge-large is hard-capped at 512 tokens and token density
// varies (EVE wiki ~2.2–3.4 chars/token), so a fixed char cap can't guarantee a
// fit. We truncate at maxEmbedChars and retry with 3/4 of the text on any
// over-context response until it fits (up to 8 attempts).
type openAIEmbedClientRag struct {
	baseURL string // includes the /v1 suffix, e.g. http://127.0.0.1:8082/v1
	model   string
	apiKey  string // empty is fine for local llama-server
	http    *http.Client
}

// maxEmbedCharsRag guards against exceeding the embed model's 512-token context.
// Dense wiki prose tokenizes at ~3.0–3.4 chars/token (a 1900-char chunk hit 556
// tokens in testing), so the cap is set so even 3.0 chars/token stays < 512:
// 1500 / 3.0 = 500 tokens.
const maxEmbedCharsRag = 1500

// NewOpenAIEmbedClient builds an embedding client for an OpenAI-compatible endpoint.
// Pass the base URL (e.g. "http://127.0.0.1:8082/v1" or "https://api.deepinfra.com/v1"),
// the model name (e.g. "BAAI/bge-large-en-v1.5"), and an API key (empty for local
// llama-server, DEEPINFRA_API_KEY for cloud).
func NewOpenAIEmbedClient(baseURL, model, apiKey string) ports.Embedder {
	return &openAIEmbedClientRag{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Embed embeds text, shrinking the input and retrying if the model rejects it
// for exceeding its context window.
func (c *openAIEmbedClientRag) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) > maxEmbedCharsRag {
		text = text[:maxEmbedCharsRag]
	}
	for attempt := 0; attempt < 8; attempt++ {
		vec, status, body, err := c.embedOnce(ctx, text)
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			return vec, nil
		}
		// Over-context rejection (HTTP 400/500 mentioning context/tokens): shrink and retry.
		overSize := strings.Contains(body, "context") || strings.Contains(body, "too large") || strings.Contains(body, "token")
		if overSize && len(text) > 200 {
			text = text[:len(text)*3/4]
			continue
		}
		return nil, fmt.Errorf("openai embed HTTP %d: %s", status, body)
	}
	return nil, fmt.Errorf("openai embed: input still too large after shrinking")
}

// embedOnce performs one embeddings request, returning the vector on 200 or the
// status + body otherwise (so the caller can decide whether to shrink + retry).
func (c *openAIEmbedClientRag) embedOnce(ctx context.Context, text string) ([]float32, int, string, error) {
	body, _ := json.Marshal(map[string]any{"model": c.model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, string(raw), nil
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, "", fmt.Errorf("openai embed decode: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, 0, "", fmt.Errorf("openai embed: empty embedding in response")
	}
	return out.Data[0].Embedding, http.StatusOK, "", nil
}
