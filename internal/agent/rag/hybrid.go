package rag

import (
	"context"
	"fmt"
	"sort"

	"github.com/castwell/forge/internal/agent/core"
)

// HybridRetriever implements core.Retriever using vector + BM25 hybrid search
// with Reciprocal Rank Fusion (RRF).
type HybridRetriever struct {
	store    DocumentStore
	embedder Embedder
	k        int // RRF constant, default 60
}

// DocumentStore abstracts the database layer for document storage and search.
// This allows testing without a real PostgreSQL+pgvector instance.
// TODO(AE-3-deploy): implement PgDocumentStore with pgxpool + pgvector cosine search + BM25 ts_rank.
type DocumentStore interface {
	// VectorSearch returns documents ordered by cosine similarity to the embedding.
	VectorSearch(ctx context.Context, embedding []float32, limit int) ([]core.Document, error)
	// BM25Search returns documents ordered by BM25 full-text relevance.
	BM25Search(ctx context.Context, query string, limit int) ([]core.Document, error)
	// Upsert inserts or updates a document.
	Upsert(ctx context.Context, doc core.Document, embedding []float32) error
}

// NewHybridRetriever creates a HybridRetriever.
func NewHybridRetriever(store DocumentStore, embedder Embedder) *HybridRetriever {
	return &HybridRetriever{
		store:    store,
		embedder: embedder,
		k:        60,
	}
}

// Search implements core.Retriever.Search.
func (r *HybridRetriever) Search(ctx context.Context, query string, topK int) ([]core.Document, error) {
	if topK <= 0 {
		topK = 5
	}

	embedding, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: embed query: %w", err)
	}

	candidateK := topK * 2
	vectorDocs, err := r.store.VectorSearch(ctx, embedding, candidateK)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: vector search: %w", err)
	}

	bm25Docs, err := r.store.BM25Search(ctx, query, candidateK)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: BM25 search: %w", err)
	}

	fused := reciprocalRankFusion(vectorDocs, bm25Docs, r.k)

	if len(fused) > topK {
		fused = fused[:topK]
	}
	return fused, nil
}

// Index implements core.Retriever.Index.
func (r *HybridRetriever) Index(ctx context.Context, docs []core.Document) error {
	for _, doc := range docs {
		embedding, err := r.embedder.Embed(ctx, doc.Content)
		if err != nil {
			return fmt.Errorf("index document %s: embed: %w", doc.ID, err)
		}
		if err := r.store.Upsert(ctx, doc, embedding); err != nil {
			return fmt.Errorf("index document %s: upsert: %w", doc.ID, err)
		}
	}
	return nil
}

// reciprocalRankFusion merges two ranked lists using the RRF formula:
// score(d) = Σ 1/(k + rank_i(d))
func reciprocalRankFusion(listA, listB []core.Document, k int) []core.Document {
	scores := make(map[string]float64)
	docs := make(map[string]core.Document)

	for rank, doc := range listA {
		scores[doc.ID] += 1.0 / float64(k+rank+1)
		docs[doc.ID] = doc
	}
	for rank, doc := range listB {
		scores[doc.ID] += 1.0 / float64(k+rank+1)
		if _, exists := docs[doc.ID]; !exists {
			docs[doc.ID] = doc
		}
	}

	result := make([]core.Document, 0, len(docs))
	for id, doc := range docs {
		doc.Score = scores[id]
		result = append(result, doc)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	return result
}

// --- In-memory test implementations ---

// InMemoryDocumentStore is a test implementation of DocumentStore.
type InMemoryDocumentStore struct {
	docs       []core.Document
	embeddings map[string][]float32
}

// NewInMemoryDocumentStore creates an in-memory store for testing.
func NewInMemoryDocumentStore() *InMemoryDocumentStore {
	return &InMemoryDocumentStore{
		embeddings: make(map[string][]float32),
	}
}

// Upsert adds or updates a document in memory.
func (s *InMemoryDocumentStore) Upsert(_ context.Context, doc core.Document, embedding []float32) error {
	for i, d := range s.docs {
		if d.ID == doc.ID {
			s.docs[i] = doc
			s.embeddings[doc.ID] = embedding
			return nil
		}
	}
	s.docs = append(s.docs, doc)
	s.embeddings[doc.ID] = embedding
	return nil
}

// VectorSearch returns all docs (mock: no actual cosine similarity).
func (s *InMemoryDocumentStore) VectorSearch(_ context.Context, _ []float32, limit int) ([]core.Document, error) {
	if limit > len(s.docs) {
		limit = len(s.docs)
	}
	result := make([]core.Document, limit)
	copy(result, s.docs[:limit])
	return result, nil
}

// BM25Search returns docs containing the query substring.
func (s *InMemoryDocumentStore) BM25Search(_ context.Context, query string, limit int) ([]core.Document, error) {
	var result []core.Document
	for _, doc := range s.docs {
		if len(result) >= limit {
			break
		}
		if containsSubstring(doc.Content, query) {
			result = append(result, doc)
		}
	}
	return result, nil
}

func containsSubstring(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
