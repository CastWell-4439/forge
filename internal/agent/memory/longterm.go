package memory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
		ID:       entry.ID,
		Content:  entry.Content,
		Metadata: memoryMetadata(entry),
	}

	embedding, err := m.embedder.Embed(ctx, entry.Content)
	if err != nil {
		return fmt.Errorf("long-term memory save: embed: %w", err)
	}

	return m.store.Upsert(ctx, doc, embedding)
}

// memoryMetadata renders an entry's governance fields into the store's
// key/value metadata.
//
// The document store predates the governance work and carries a flat
// map[string]string, so the fields ride in it rather than changing the stored
// format. Two things make that the right call: existing files keep loading
// (unknown keys are simply absent), and the fields become readable by anything
// that can read the file — which is what the control plane's review and
// verification need, since they read this store without importing the agent
// plane.
//
// A zero value is written as an ABSENT key rather than as "0" or "". Absence is
// what a reader already has to handle for entries written before these fields
// existed, so there is one case to get right instead of two.
func memoryMetadata(entry core.MemoryEntry) map[string]string {
	meta := map[string]string{
		"category":   entry.Category,
		"created_at": entry.CreatedAt.Format(time.RFC3339),
	}
	if entry.Source != "" {
		meta["source"] = string(entry.Source)
	}
	if entry.Confidence > 0 {
		meta["confidence"] = strconv.FormatFloat(entry.Confidence, 'f', -1, 64)
	}
	if !entry.ObservedAt.IsZero() {
		meta["observed_at"] = entry.ObservedAt.Format(time.RFC3339)
	}
	if entry.Layer != "" {
		meta["layer"] = string(entry.Layer)
	}
	return meta
}

// memoryEntryFromDoc rebuilds an entry from a stored document.
//
// Every field is optional and every parse failure falls back to the zero value:
// a malformed confidence must not make a memory unreadable, because the memory
// itself is the valuable part and the metadata only helps judge it.
func memoryEntryFromDoc(doc core.Document) core.MemoryEntry {
	entry := core.MemoryEntry{
		ID:      doc.ID,
		Content: doc.Content,
	}
	if doc.Metadata == nil {
		// No metadata at all is the oldest possible document. It still gets the
		// default layer, so a caller can treat layer uniformly instead of
		// checking for the empty string everywhere.
		entry.Layer = core.NormalizeMemoryLayer("")
		return entry
	}
	entry.Category = doc.Metadata["category"]
	entry.Source = core.MemorySource(doc.Metadata["source"])
	if raw := doc.Metadata["confidence"]; raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			entry.Confidence = v
		}
	}
	if raw := doc.Metadata["observed_at"]; raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			entry.ObservedAt = t
		}
	}
	if raw := doc.Metadata["created_at"]; raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			entry.CreatedAt = t
		}
	}
	entry.Layer = core.NormalizeMemoryLayer(doc.Metadata["layer"])
	return entry
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
		// The full entry, metadata included. This used to rebuild only id,
		// content and category, which meant the governance fields added later
		// were written on save and then dropped on read — a memory's source,
		// confidence and layer were lost the moment it was recalled, so every
		// cross-run judgement about it (agreement, staleness, layer) had no data.
		entries[i] = memoryEntryFromDoc(doc)
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
