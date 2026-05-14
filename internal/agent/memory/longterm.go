package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/rag"
)

// LongTermMemory stores persistent experience/knowledge via pgvector.
// Reuses the RAG infrastructure (Embedder + DocumentStore).
type LongTermMemory struct {
	store    rag.DocumentStore
	embedder rag.Embedder
}

// NewLongTermMemory creates a long-term memory backed by RAG components.
func NewLongTermMemory(store rag.DocumentStore, embedder rag.Embedder) *LongTermMemory {
	return &LongTermMemory{
		store:    store,
		embedder: embedder,
	}
}

// Save persists a memory entry as a document with category "memory".
func (m *LongTermMemory) Save(ctx context.Context, entry core.MemoryEntry) error {
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	doc := core.Document{
		ID:      entry.ID,
		Content: entry.Content,
		Metadata: map[string]string{
			"category":   entry.Category,
			"created_at": entry.CreatedAt.Format(time.RFC3339),
		},
	}

	embedding, err := m.embedder.Embed(ctx, entry.Content)
	if err != nil {
		return fmt.Errorf("long-term memory save: embed: %w", err)
	}

	return m.store.Upsert(ctx, doc, embedding)
}

// Search finds relevant memories by semantic similarity.
func (m *LongTermMemory) Search(ctx context.Context, query string, topK int) ([]core.MemoryEntry, error) {
	if topK <= 0 {
		topK = 5
	}

	embedding, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("long-term memory search: embed: %w", err)
	}

	docs, err := m.store.VectorSearch(ctx, embedding, topK)
	if err != nil {
		return nil, fmt.Errorf("long-term memory search: %w", err)
	}

	entries := make([]core.MemoryEntry, len(docs))
	for i, doc := range docs {
		cat := ""
		if doc.Metadata != nil {
			cat = doc.Metadata["category"]
		}
		entries[i] = core.MemoryEntry{
			ID:       doc.ID,
			Content:  doc.Content,
			Category: cat,
		}
	}
	return entries, nil
}

// --- In-memory LongTermMemory for testing ---

// InMemoryLongTerm is a test implementation that doesn't need pgvector.
type InMemoryLongTerm struct {
	mu      sync.RWMutex
	entries []core.MemoryEntry
}

// NewInMemoryLongTerm creates a test long-term memory.
func NewInMemoryLongTerm() *InMemoryLongTerm {
	return &InMemoryLongTerm{}
}

// Save stores an entry in memory.
func (m *InMemoryLongTerm) Save(_ context.Context, entry core.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	m.entries = append(m.entries, entry)
	return nil
}

// Search returns entries whose content contains the query.
func (m *InMemoryLongTerm) Search(_ context.Context, query string, topK int) ([]core.MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type scored struct {
		entry core.MemoryEntry
		score int
	}
	var matches []scored
	q := strings.ToLower(query)
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.Content), q) {
			matches = append(matches, scored{entry: e, score: 1})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if len(matches) > topK {
		matches = matches[:topK]
	}
	result := make([]core.MemoryEntry, len(matches))
	for i, m := range matches {
		result[i] = m.entry
	}
	return result, nil
}
