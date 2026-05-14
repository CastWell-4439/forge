package guardrails

import (
	"context"
	"fmt"
	"sync"
)

// ErrBudgetExceeded is returned when a session exceeds its token budget.
var ErrBudgetExceeded = fmt.Errorf("token budget exceeded")

// BudgetEnforcer tracks per-session token consumption and enforces limits.
// Production version would use Redis; this implementation is in-memory for testing.
// TODO(AE-4-deploy): implement Redis-backed BudgetEnforcer with TTL 24h.
type BudgetEnforcer struct {
	mu           sync.Mutex
	usage        map[string]int64 // session_id → total tokens consumed
	limits       map[string]int64 // session_id → custom limit (optional)
	defaultLimit int64            // default 100k tokens
}

// NewBudgetEnforcer creates an enforcer with the given default token limit.
func NewBudgetEnforcer(defaultLimit int64) *BudgetEnforcer {
	if defaultLimit <= 0 {
		defaultLimit = 100000
	}
	return &BudgetEnforcer{
		usage:        make(map[string]int64),
		limits:       make(map[string]int64),
		defaultLimit: defaultLimit,
	}
}

// SetLimit sets a custom token budget for a specific session.
func (b *BudgetEnforcer) SetLimit(sessionID string, limit int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limits[sessionID] = limit
}

// Check verifies if the session has budget remaining.
// Implements core.BudgetChecker.
func (b *BudgetEnforcer) Check(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	limit := b.defaultLimit
	if custom, ok := b.limits[sessionID]; ok {
		limit = custom
	}

	if b.usage[sessionID] >= limit {
		return fmt.Errorf("%w: session %s used %d/%d tokens",
			ErrBudgetExceeded, sessionID, b.usage[sessionID], limit)
	}
	return nil
}

// Record adds token consumption for a session.
func (b *BudgetEnforcer) Record(ctx context.Context, sessionID string, tokens int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage[sessionID] += tokens
	return nil
}

// Usage returns current token usage for a session.
func (b *BudgetEnforcer) Usage(sessionID string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage[sessionID]
}

// Reset clears usage for a session.
func (b *BudgetEnforcer) Reset(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.usage, sessionID)
}
