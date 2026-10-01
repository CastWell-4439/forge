package rag

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// HybridRetriever implements core.Retriever as a two-stage retrieval pipeline:
//
//	recall:  vector (bi-encoder) + BM25 → RRF fusion        [widening]
//	rank:    optional cross-encoder rerank over the candidates [narrowing]
//
// Every stage is optional in the honest direction: no embedder means BM25-only,
// a failed embed means BM25-only, no reranker means fusion order ships. The
// pipeline never fabricates a stage it does not have, and a search never fails
// because an optional stage is missing.
type HybridRetriever struct {
	store    DocumentStore
	embedder Embedder // may be nil
	reranker Reranker // may be nil
	k        int      // RRF constant, default 60

	mu       sync.Mutex
	lastMode string // diagnostics for SearchMode()
}

// DocumentStore abstracts the database layer for document storage and search.
// FileDocumentStore is the default; a PostgreSQL+pgvector variant can implement
// the same three methods later without touching this pipeline.
type DocumentStore interface {
	// VectorSearch returns documents ordered by cosine similarity to the embedding.
	VectorSearch(ctx context.Context, embedding []float32, limit int) ([]core.Document, error)
	// BM25Search returns documents ordered by BM25 full-text relevance.
	BM25Search(ctx context.Context, query string, limit int) ([]core.Document, error)
	// Upsert inserts or updates a document.
	Upsert(ctx context.Context, doc core.Document, embedding []float32) error
}

// NewHybridRetriever creates a HybridRetriever. A nil embedder is legal: the
// pipeline degrades to BM25-only and says so via SearchMode.
func NewHybridRetriever(store DocumentStore, embedder Embedder) *HybridRetriever {
	return &HybridRetriever{
		store:    store,
		embedder: embedder,
		k:        60,
		lastMode: "bm25-only:no-embedder",
	}
}

// WithReranker attaches a cross-encoder for the ranking stage. It returns the
// retriever so assembly reads top-down, and chaining keeps the constructor
// signature stable.
func (r *HybridRetriever) WithReranker(reranker Reranker) *HybridRetriever {
	r.reranker = reranker
	return r
}

// SearchMode reports how the most recent Search was executed, for callers that
// surface retrieval provenance (knowledge.search prints it). It is diagnostics,
// not a contract: under concurrency it reflects whichever search finished last.
func (r *HybridRetriever) SearchMode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastMode
}

func (r *HybridRetriever) setMode(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastMode = mode
}

// queryEmbed encodes the query through the query tower when the embedder has
// one (E5/BGE-style prefixes differ between query and passage), and plain Embed
// otherwise.
func (r *HybridRetriever) queryEmbed(ctx context.Context, text string) ([]float32, error) {
	if tower, ok := r.embedder.(TowerEmbedder); ok {
		return tower.EmbedQuery(ctx, text)
	}
	return r.embedder.Embed(ctx, text)
}

// docEmbed encodes a document through the document tower when available.
func (r *HybridRetriever) docEmbed(ctx context.Context, text string) ([]float32, error) {
	if tower, ok := r.embedder.(TowerEmbedder); ok {
		return tower.EmbedDoc(ctx, text)
	}
	return r.embedder.Embed(ctx, text)
}

// Search implements core.Retriever.Search.
func (r *HybridRetriever) Search(ctx context.Context, query string, topK int) ([]core.Document, error) {
	if topK <= 0 {
		topK = 5
	}

	// --- Recall ---
	var fused []core.Document
	mode := "hybrid"

	if r.embedder == nil {
		// No query tower at all: BM25 is the whole pipeline, and that is stated,
		// not hidden behind a fake vector pass.
		mode = "bm25-only:no-embedder"
		bm25Docs, err := r.store.BM25Search(ctx, query, topK*2)
		if err != nil {
			return nil, fmt.Errorf("hybrid search: BM25 search: %w", err)
		}
		fused = bm25Docs
	} else {
		embedding, err := r.queryEmbed(ctx, query)
		if err != nil {
			// The vector tower is unreachable (no endpoint, timeout): BM25 still
			// answers, so the search degrades instead of failing.
			mode = "bm25-only:embed-failed"
			bm25Docs, bm25Err := r.store.BM25Search(ctx, query, topK*2)
			if bm25Err != nil {
				return nil, fmt.Errorf("hybrid search: BM25 search after embed failure (%v): %w", err, bm25Err)
			}
			fused = bm25Docs
		} else {
			vectorDocs, err := r.store.VectorSearch(ctx, embedding, topK*2)
			if err != nil {
				return nil, fmt.Errorf("hybrid search: vector search: %w", err)
			}
			bm25Docs, err := r.store.BM25Search(ctx, query, topK*2)
			if err != nil {
				return nil, fmt.Errorf("hybrid search: BM25 search: %w", err)
			}
			fused = reciprocalRankFusion(vectorDocs, bm25Docs, r.k)
		}
	}

	if len(fused) > topK*2 {
		fused = fused[:topK*2]
	}

	// --- Rank ---
	if r.reranker != nil && len(fused) > 0 {
		// Stamp the recall score onto copies before reranking. The trace belongs
		// to the pipeline, not to a particular reranker implementation, and the
		// metadata map is cloned because the store shares it by reference.
		for i := range fused {
			meta := make(map[string]string, len(fused[i].Metadata)+1)
			for k, v := range fused[i].Metadata {
				meta[k] = v
			}
			meta["rrf_score"] = trimFloat(fused[i].Score)
			fused[i].Metadata = meta
		}
		reranked, err := r.reranker.Rerank(ctx, query, fused, topK)
		if err == nil {
			fused = reranked
			mode += "+rerank"
		} else {
			// A reranker that failed leaves the fusion order intact; the mode
			// records the attempt so a changed ranking is explicable.
			mode += "~rerank-failed"
		}
	}

	if len(fused) > topK {
		fused = fused[:topK]
	}
	r.setMode(mode)
	return fused, nil
}

// Index implements core.Retriever.Index.
func (r *HybridRetriever) Index(ctx context.Context, docs []core.Document) error {
	for _, doc := range docs {
		var embedding []float32
		if r.embedder != nil {
			var err error
			embedding, err = r.docEmbed(ctx, doc.Content)
			if err != nil {
				// Index without a vector is still indexable: BM25 can find it,
				// and the vector pass simply skips a document it has no vector
				// for. The alternative - rejecting the document - would silently
				// shrink the knowledge base whenever the endpoint hiccups.
				embedding = nil
			}
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

// --- In-memory store (tests, ephemeral runs) ---
//
// It uses the same ranking primitives as FileDocumentStore: the only difference
// is that nothing is persisted. The previous version of this store returned the
// first N documents for any vector query and called it search.

// InMemoryDocumentStore is a non-persistent DocumentStore with real algorithms.
type InMemoryDocumentStore struct {
	mu   sync.Mutex
	docs []core.Document
	vecs map[string][]float32
}

// NewInMemoryDocumentStore creates an in-memory store.
func NewInMemoryDocumentStore() *InMemoryDocumentStore {
	return &InMemoryDocumentStore{vecs: make(map[string][]float32)}
}

// Upsert adds or updates a document in memory.
func (s *InMemoryDocumentStore) Upsert(_ context.Context, doc core.Document, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.docs {
		if d.ID == doc.ID {
			s.docs[i] = doc
			if len(embedding) > 0 {
				s.vecs[doc.ID] = embedding
			} else {
				delete(s.vecs, doc.ID)
			}
			return nil
		}
	}
	s.docs = append(s.docs, doc)
	if len(embedding) > 0 {
		s.vecs[doc.ID] = embedding
	}
	return nil
}

// VectorSearch ranks by real cosine similarity; documents without an embedding
// are skipped rather than pretending to match.
func (s *InMemoryDocumentStore) VectorSearch(_ context.Context, embedding []float32, limit int) ([]core.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type hit struct {
		doc   core.Document
		score float64
	}
	var hits []hit
	for _, doc := range s.docs {
		vec, ok := s.vecs[doc.ID]
		if !ok {
			continue
		}
		doc.Score = cosine(embedding, vec)
		hits = append(hits, hit{doc, doc.Score})
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].score > hits[j-1].score; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]core.Document, len(hits))
	for i, h := range hits {
		out[i] = h.doc
	}
	return out, nil
}

// BM25Search ranks with the same Okapi BM25 as the file store.
func (s *InMemoryDocumentStore) BM25Search(_ context.Context, query string, limit int) ([]core.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ranked := bm25Rank(s.docs, query)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}
