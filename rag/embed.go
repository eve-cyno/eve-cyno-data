package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"eve-cyno.dev/go/data/ports"
)

// ollamaEmbedClient implements ports.Embedder.Embed via local Ollama /api/embed.
type ollamaEmbedClient struct {
	host      string
	model     string
	keepAlive string
	http      *http.Client
}

// NewOllamaEmbedClient creates an embedding client for local Ollama.
func NewOllamaEmbedClient(host, model string) ports.Embedder {
	return &ollamaEmbedClient{
		host:      host,
		model:     model,
		keepAlive: "5m",
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ollamaEmbedClient) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      c.model,
		"input":      text, // single-string → legacy {"embedding": [...]} shape
		"keep_alive": c.keepAlive,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ollama embed HTTP %d: %s", resp.StatusCode, string(raw))
	}

	// Handle both single-input shape {"embedding": [...]} and batch {"embeddings": [[...]]}
	var single struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && len(single.Embedding) > 0 {
		return single.Embedding, nil
	}
	var batch struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &batch); err == nil && len(batch.Embeddings) > 0 {
		return batch.Embeddings[0], nil
	}
	return nil, fmt.Errorf("ollama embed: unrecognized response shape")
}

// deepInfraEmbedClient implements ports.Embedder.Embed via DeepInfra OpenAI-compatible /embeddings.
type deepInfraEmbedClient struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewDeepInfraEmbedClient creates an embedding client for DeepInfra.
func NewDeepInfraEmbedClient(apiKey, model, baseURL string) ports.Embedder {
	if baseURL == "" {
		baseURL = "https://api.deepinfra.com/v1/openai"
	}
	return &deepInfraEmbedClient{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *deepInfraEmbedClient) Embed(ctx context.Context, text string) ([]float32, error) {
	// Truncate at 1200 chars to prevent 400 errors (mirrors Python hard-cap)
	if len(text) > 1200 {
		text = text[:1200]
	}
	body, _ := json.Marshal(map[string]any{
		"model": c.model,
		"input": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("deepinfra embed HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Data) == 0 {
		return nil, fmt.Errorf("deepinfra embed: parse error or empty response")
	}
	return result.Data[0].Embedding, nil
}
