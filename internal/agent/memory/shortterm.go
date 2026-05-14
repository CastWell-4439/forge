// Package memory implements short-term and long-term agent memory (M5).
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// ShortTermMemory stores ephemeral per-session data.
// Production uses Redis; InMemoryShortTerm is for testing.
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

// RedisShortTerm is the production implementation using Redis.
// Key format: forge:memory:short:{session_id}:{key}
// TODO(AE-3-deploy): inject real redis.Client, implement Save/Get/GetAll with TTL.
type RedisShortTerm struct {
	// redis client would go here; placeholder for now.
	prefix string
	ttl    time.Duration
}

// NewRedisShortTerm creates a Redis-backed short-term memory (stub).
func NewRedisShortTerm(prefix string, ttl time.Duration) *RedisShortTerm {
	return &RedisShortTerm{prefix: prefix, ttl: ttl}
}

func (r *RedisShortTerm) Save(_ context.Context, _, _ string, _ any) error {
	return fmt.Errorf("RedisShortTerm: not yet connected to Redis")
}

func (r *RedisShortTerm) Get(_ context.Context, _, _ string) (any, error) {
	return nil, fmt.Errorf("RedisShortTerm: not yet connected to Redis")
}

func (r *RedisShortTerm) GetAll(_ context.Context, _ string) (map[string]any, error) {
	return nil, fmt.Errorf("RedisShortTerm: not yet connected to Redis")
}

// --- JSON serialization helpers for Redis ---

func marshalValue(v any) ([]byte, error) {
	return json.Marshal(v)
}

func unmarshalValue(data []byte) (any, error) {
	var v any
	err := json.Unmarshal(data, &v)
	return v, err
}

// ensure InMemoryShortTerm satisfies ShortTermMemory
var _ ShortTermMemory = (*InMemoryShortTerm)(nil)
var _ ShortTermMemory = (*RedisShortTerm)(nil)

// Ensure unused imports don't cause errors.
var _ = marshalValue
var _ = unmarshalValue

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
