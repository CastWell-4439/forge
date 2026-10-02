// Package memory implements short-term and long-term agent memory (M5).
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// ShortTermMemory stores ephemeral per-session data.
// InMemoryShortTerm is the only implementation: short-term memory is
// process-local by design, and anything meant to survive a restart belongs in
// the checkpoint or the long-term store instead.
type ShortTermMemory interface {
	Save(ctx context.Context, sessionID, key string, value any) error
	Get(ctx context.Context, sessionID, key string) (any, error)
	GetAll(ctx context.Context, sessionID string) (map[string]any, error)
}

// InMemoryShortTerm is a test/dev implementation backed by a sync.Map.
type InMemoryShortTerm struct {
	mu   sync.RWMutex
	data map[string]map[string]any // sessionID -> key -> value
	ttl  time.Duration
}

// NewInMemoryShortTerm creates an in-memory short-term store.
func NewInMemoryShortTerm(ttl time.Duration) *InMemoryShortTerm {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &InMemoryShortTerm{
		data: make(map[string]map[string]any),
		ttl:  ttl,
	}
}

func (m *InMemoryShortTerm) Save(_ context.Context, sessionID, key string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[sessionID]; !ok {
		m.data[sessionID] = make(map[string]any)
	}
	m.data[sessionID][key] = value
	return nil
}

func (m *InMemoryShortTerm) Get(_ context.Context, sessionID, key string) (any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.data[sessionID]
	if !ok {
		return nil, nil
	}
	return sess[key], nil
}

func (m *InMemoryShortTerm) GetAll(_ context.Context, sessionID string) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.data[sessionID]
	if !ok {
		return nil, nil
	}
	result := make(map[string]any, len(sess))
	for k, v := range sess {
		result[k] = v
	}
	return result, nil
}

// ensure InMemoryShortTerm satisfies ShortTermMemory
var _ ShortTermMemory = (*InMemoryShortTerm)(nil)

// --- core.MemoryStore adapter ---

// Store implements core.MemoryStore by combining ShortTermMemory + LongTermMemory.
type Store struct {
	short ShortTermMemory
	long  *LongTermMemory
}

// NewStore creates a unified memory store.
func NewStore(short ShortTermMemory, long *LongTermMemory) *Store {
	return &Store{short: short, long: long}
}

// SaveShortTerm implements core.MemoryStore.
func (s *Store) SaveShortTerm(ctx context.Context, sessionID, key string, value any) error {
	if s.short == nil {
		return nil
	}
	return s.short.Save(ctx, sessionID, key, value)
}

// GetShortTerm implements core.MemoryStore.
func (s *Store) GetShortTerm(ctx context.Context, sessionID, key string) (any, error) {
	if s.short == nil {
		return nil, nil
	}
	return s.short.Get(ctx, sessionID, key)
}

// SaveLongTerm implements core.MemoryStore.
func (s *Store) SaveLongTerm(ctx context.Context, entry core.MemoryEntry) error {
	if s.long == nil {
		return nil
	}
	return s.long.Save(ctx, entry)
}

// SearchLongTerm implements core.MemoryStore.
func (s *Store) SearchLongTerm(ctx context.Context, query string, topK int) ([]core.MemoryEntry, error) {
	if s.long == nil {
		return nil, nil
	}
	return s.long.Search(ctx, query, topK)
}

// Ensure Store satisfies core.MemoryStore.
var _ core.MemoryStore = (*Store)(nil)
