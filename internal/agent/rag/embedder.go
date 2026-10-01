// Package rag implements Retrieval-Augmented Generation with hybrid search.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder generates vector embeddings from text.
type Embedder interface {
	// Embed returns the embedding vector for a single text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch returns embeddings for multiple texts.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// TowerEmbedder is the two-tower view of an embedder: query and passage towers
// are encoded separately, which is mandatory for E5/BGE-style models where a
// missing "query: "/"passage: " prefix silently destroys ranking quality.
// Embedders that need no prefixes satisfy it trivially (prefix = "").
type TowerEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	EmbedDoc(ctx context.Context, text string) ([]float32, error)
}

// EmbedderFromEnv assembles an embedder from FORGE_EMBEDDING_* variables
// (falling back to the chat client's FORGE_LLM_* for base URL and key), or
// returns nil when no endpoint is configured. A nil embedder sends the pipeline
// into its documented BM25-only mode instead of failing.
func EmbedderFromEnv() Embedder {
	baseURL := firstNonEmpty(os.Getenv("FORGE_EMBEDDING_BASE_URL"), os.Getenv("FORGE_LLM_BASE_URL"))
	apiKey := firstNonEmpty(os.Getenv("FORGE_EMBEDDING_API_KEY"), os.Getenv("FORGE_LLM_API_KEY"))
	if baseURL == "" {
		return nil
	}
	model := os.Getenv("FORGE_EMBEDDING_MODEL")
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &LLMEmbedder{
		client:      &http.Client{Timeout: 30 * time.Second},
		baseURL:     baseURL,
		apiKey:      apiKey,
		model:       model,
		QueryPrefix: os.Getenv("FORGE_EMBEDDING_QUERY_PREFIX"),
		DocPrefix:   os.Getenv("FORGE_EMBEDDING_DOC_PREFIX"),
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// LLMEmbedder implements Embedder via an OpenAI-compatible embedding API.
type LLMEmbedder struct {
	client *http.Client
	// baseURL follows the same convention as harness.LLMConfig.BaseURL and
	// already includes the API version, e.g. "https://host/v1".
	baseURL string
	apiKey  string
	model   string // e.g. "text-embedding-ada-002"

	// QueryPrefix and DocPrefix are the two towers: set them to the model's
	// convention ("query: ", "passage: ") to make two-tower encoding correct.
	QueryPrefix string
	DocPrefix   string
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

// EmbedQuery encodes through the query tower (QueryPrefix applied).
func (e *LLMEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return e.Embed(ctx, e.QueryPrefix+text)
}

// EmbedDoc encodes through the document tower (DocPrefix applied).
func (e *LLMEmbedder) EmbedDoc(ctx context.Context, text string) ([]float32, error) {
	return e.Embed(ctx, e.DocPrefix+text)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(e.baseURL, "/")+"/embeddings", bytes.NewReader(body))
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

// MockEmbedder produces deterministic bag-of-words feature-hash vectors: each
// token bumps one dimension and the result is L2-normalized. The previous
// zero-vector version made every cosine score 0, so "vector search" in tests
// never actually ranked anything. With this one, texts that share tokens score
// higher than texts that do not - enough for ranking tests to be meaningful
// without a network.
type MockEmbedder struct {
	Dimension int
}

func (m *MockEmbedder) dim() int {
	if m.Dimension > 0 {
		return m.Dimension
	}
	return 128
}

// Embed returns the feature-hash vector for one text.
func (m *MockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	dim := m.dim()
	vec := make([]float32, dim)
	for _, token := range tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(token))
		vec[h.Sum32()%uint32(dim)]++
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= scale
		}
	}
	return vec, nil
}

// EmbedBatch returns feature-hash vectors for all inputs.
func (m *MockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := m.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}
