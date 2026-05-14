// Package rag implements Retrieval-Augmented Generation with hybrid search.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Embedder generates vector embeddings from text.
type Embedder interface {
	// Embed returns the embedding vector for a single text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch returns embeddings for multiple texts.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// LLMEmbedder implements Embedder via an OpenAI-compatible embedding API.
// TODO(AE-3-deploy): configure baseURL/apiKey from bmc-llm-relay embedding endpoint.
type LLMEmbedder struct {
	client  *http.Client
	baseURL string // e.g. "https://bmc-llm-relay..."
	apiKey  string
	model   string // e.g. "text-embedding-ada-002"
}

// NewLLMEmbedder creates an LLMEmbedder.
func NewLLMEmbedder(baseURL, apiKey, model string) *LLMEmbedder {
	return &LLMEmbedder{
		client:  &http.Client{},
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
	}
}

// embeddingRequest is the OpenAI-compatible request body.
type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// embeddingResponse is the OpenAI-compatible response body.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed returns the embedding for a single text.
func (e *LLMEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("embedder: empty response")
	}
	return results[0], nil
}

// EmbedBatch returns embeddings for multiple texts in a single API call.
func (e *LLMEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embeddingRequest{
		Input: texts,
		Model: e.model,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("embedder: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedder: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder: API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedder: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("embedder: decode response: %w", err)
	}

	results := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < len(results) {
			results[d.Index] = d.Embedding
		}
	}
	return results, nil
}

// MockEmbedder returns fixed-length zero vectors. For testing only.
type MockEmbedder struct {
	Dimension int
}

// Embed returns a zero vector.
func (m *MockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, m.Dimension), nil
}

// EmbedBatch returns zero vectors for all inputs.
func (m *MockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i := range texts {
		results[i] = make([]float32, m.Dimension)
	}
	return results, nil
}
