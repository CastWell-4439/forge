package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// Reranker re-scores recalled candidates with a cross-encoder: query and
// document are encoded together, so the model can weigh whether the document
// actually answers the query instead of merely sharing its topic. That is the
// difference a bi-encoder cannot make - which is why a reranker runs over the
// small candidate set two-tower recall produced, never over the whole store.
type Reranker interface {
	// Rerank orders docs best-first and returns at most topK of them.
	// Implementations only reorder; they never widen the candidate set.
	Rerank(ctx context.Context, query string, docs []core.Document, topK int) ([]core.Document, error)
}

// HTTPReranker calls a reranking endpoint. The request/response shape follows
// the common convention:
//
//	POST {endpoint}
//	{"model": "...", "query": "...", "documents": ["...", ...]}
//	→ {"results": [{"index": 0, "relevance_score": 0.87}, ...]}
//
// It is assembled from the environment (RerankerFromEnv), same as the embedder,
// so nothing is configured from inside the control plane.
type HTTPReranker struct {
	Endpoint string
	APIKey   string
	Model    string
	Client   *http.Client
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

// RerankerFromEnv builds a reranker from FORGE_RERANK_* variables, or returns
// nil when none is configured: an absent reranker means fusion order ships
// unchanged, not that something is broken.
func RerankerFromEnv() Reranker {
	endpoint := os.Getenv("FORGE_RERANK_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	return &HTTPReranker{
		Endpoint: endpoint,
		APIKey:   os.Getenv("FORGE_RERANK_API_KEY"),
		Model:    envOr("FORGE_RERANK_MODEL", "rerank-v2"),
		Client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Rerank implements the Reranker interface.
func (h *HTTPReranker) Rerank(ctx context.Context, query string, docs []core.Document, topK int) ([]core.Document, error) {
	if len(docs) == 0 {
		return docs, nil
	}
	payload, err := json.Marshal(rerankRequest{
		Model:     h.Model,
		Query:     query,
		Documents: documentContents(docs),
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIKey)
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("rerank: endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("rerank: decode: %w", err)
	}

	return applyRerankScores(docs, parsed.Results, topK), nil
}

// applyRerankScores reorders the candidates by their cross-encoder scores.
// Documents the endpoint skipped score last and keep their recall order among
// themselves. The recall-score trace (rrf_score) is stamped by the pipeline
// before any reranker runs, so this function stays pure ordering.
func applyRerankScores(docs []core.Document, results []rerankResult, topK int) []core.Document {
	scored := make(map[int]float64, len(results))
	for _, r := range results {
		if r.Index >= 0 && r.Index < len(docs) {
			scored[r.Index] = r.RelevanceScore
		}
	}

	type item struct {
		order  int
		doc    core.Document
		score  float64
		scored bool
	}
	items := make([]item, len(docs))
	for i, d := range docs {
		s, ok := scored[i]
		items[i] = item{order: i, doc: d, score: s, scored: ok}
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].scored != items[b].scored {
			return items[a].scored // scored before unscored
		}
		if items[a].scored && items[a].score != items[b].score {
			return items[a].score > items[b].score
		}
		return items[a].order < items[b].order
	})

	if topK > 0 && len(items) > topK {
		items = items[:topK]
	}
	out := make([]core.Document, len(items))
	for i, it := range items {
		out[i] = it.doc
	}
	return out
}

func documentContents(docs []core.Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Content
	}
	return out
}

func trimFloat(v float64) string {
	return fmt.Sprintf("%.6f", v)
}
