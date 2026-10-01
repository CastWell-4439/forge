package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// FileDocumentStore is the production-lean DocumentStore: real cosine vector
// search, real BM25 ranking, and JSON persistence under one root.
//
// It exists because the previous store was a test double whose "vector search"
// ignored the query embedding entirely. The alternative on the table was a
// pgvector-backed implementation, which cannot be exercised without a database;
// this one runs, survives restarts, and keeps the interface open for the PG
// variant later.
type FileDocumentStore struct {
	mu     sync.Mutex
	root   string
	docs   []core.Document
	vecs   map[string][]float32
	loaded bool
}

// NewFileDocumentStore opens (or creates) a store under root. A missing
// directory is an empty store, not an error.
func NewFileDocumentStore(root string) *FileDocumentStore {
	return &FileDocumentStore{
		root: root,
		vecs: make(map[string][]float32),
	}
}

type storedDoc struct {
	Doc       core.Document `json:"doc"`
	Embedding []float32     `json:"embedding,omitempty"`
}

func (s *FileDocumentStore) path() string {
	return filepath.Join(s.root, "documents.json")
}

// load reads the backing file once, on first use.
func (s *FileDocumentStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.loaded = true

	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("knowledge store: read %s: %w", s.path(), err)
	}
	var entries []storedDoc
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("knowledge store: parse %s: %w", s.path(), err)
	}
	for _, e := range entries {
		s.docs = append(s.docs, e.Doc)
		if len(e.Embedding) > 0 {
			s.vecs[e.Doc.ID] = e.Embedding
		}
	}
	return nil
}

// persistLocked writes the whole store atomically: temp file first, rename
// second, so an interrupted write cannot corrupt the knowledge base.
func (s *FileDocumentStore) persistLocked() error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("knowledge store: mkdir: %w", err)
	}
	entries := make([]storedDoc, 0, len(s.docs))
	for _, doc := range s.docs {
		entries = append(entries, storedDoc{Doc: doc, Embedding: s.vecs[doc.ID]})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("knowledge store: marshal: %w", err)
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("knowledge store: write: %w", err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("knowledge store: commit: %w", err)
	}
	return nil
}

// Upsert inserts or updates a document and its embedding, then persists.
func (s *FileDocumentStore) Upsert(_ context.Context, doc core.Document, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}

	replaced := false
	for i, d := range s.docs {
		if d.ID == doc.ID {
			s.docs[i] = doc
			replaced = true
			break
		}
	}
	if !replaced {
		s.docs = append(s.docs, doc)
	}
	if len(embedding) > 0 {
		s.vecs[doc.ID] = embedding
	} else {
		delete(s.vecs, doc.ID)
	}
	return s.persistLocked()
}

// VectorSearch ranks by real cosine similarity against the query embedding.
// Documents without an embedding are skipped - they have nothing to compare.
func (s *FileDocumentStore) VectorSearch(_ context.Context, embedding []float32, limit int) ([]core.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}

	type scored struct {
		doc   core.Document
		score float64
	}
	var hits []scored
	for _, doc := range s.docs {
		vec, ok := s.vecs[doc.ID]
		if !ok {
			continue
		}
		sim := cosine(embedding, vec)
		doc.Score = sim
		hits = append(hits, scored{doc, sim})
	}
	// Insertion sort: knowledge bases here are small, and stability keeps equal
	// scores in document order.
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

// BM25Search ranks by Okapi BM25 over the whole store.
func (s *FileDocumentStore) BM25Search(_ context.Context, query string, limit int) ([]core.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	ranked := bm25Rank(s.docs, query)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}
