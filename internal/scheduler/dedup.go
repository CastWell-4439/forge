package scheduler

import (
	"context"
	"sync"
	"time"
)

// Deduplicator checks whether an event has already been processed.
type Deduplicator interface {
	// IsDuplicate returns true if the event was already processed.
	IsDuplicate(ctx context.Context, triggerName, eventID string) (bool, error)
	// MarkProcessed records that an event has been processed.
	MarkProcessed(ctx context.Context, triggerName, eventID string) error
}

// InMemoryDedup is a simple in-memory deduplicator for testing and single-node deployments.
type InMemoryDedup struct {
	mu      sync.RWMutex
	records map[string]time.Time // key: "triggerName:eventID" → processed time
	ttl     time.Duration
}

// NewInMemoryDedup creates an in-memory deduplicator with the given TTL.
// Events older than TTL are eligible for garbage collection.
func NewInMemoryDedup(ttl time.Duration) *InMemoryDedup {
	return &InMemoryDedup{
		records: make(map[string]time.Time),
		ttl:     ttl,
	}
}

func (d *InMemoryDedup) IsDuplicate(_ context.Context, triggerName, eventID string) (bool, error) {
	key := triggerName + ":" + eventID
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, exists := d.records[key]
	return exists, nil
}

func (d *InMemoryDedup) MarkProcessed(_ context.Context, triggerName, eventID string) error {
	key := triggerName + ":" + eventID
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[key] = time.Now()
	return nil
}

// GC removes entries older than TTL.
func (d *InMemoryDedup) GC() {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().Add(-d.ttl)
	for k, t := range d.records {
		if t.Before(cutoff) {
			delete(d.records, k)
		}
	}
}

// Count returns the number of tracked events (for testing).
func (d *InMemoryDedup) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.records)
}

// PgDedup implements Deduplicator using PostgreSQL (trigger_checkpoints table).
// Uses the Forge storage layer for database access.
type PgDedup struct {
	queryFn func(ctx context.Context, triggerName, eventID string) (bool, error)
	insertFn func(ctx context.Context, triggerName, eventID string) error
}

// PgDedupConfig configures the PostgreSQL deduplicator.
type PgDedupConfig struct {
	// QueryFn checks if a record exists.
	QueryFn func(ctx context.Context, triggerName, eventID string) (bool, error)
	// InsertFn inserts a new checkpoint record.
	InsertFn func(ctx context.Context, triggerName, eventID string) error
}

// NewPgDedup creates a PostgreSQL-backed deduplicator.
func NewPgDedup(cfg PgDedupConfig) *PgDedup {
	return &PgDedup{
		queryFn:  cfg.QueryFn,
		insertFn: cfg.InsertFn,
	}
}

func (d *PgDedup) IsDuplicate(ctx context.Context, triggerName, eventID string) (bool, error) {
	return d.queryFn(ctx, triggerName, eventID)
}

func (d *PgDedup) MarkProcessed(ctx context.Context, triggerName, eventID string) error {
	return d.insertFn(ctx, triggerName, eventID)
}
