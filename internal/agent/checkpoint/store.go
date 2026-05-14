// Package checkpoint implements M12: Agent state persistence for crash recovery.
// Provides Save/Load/Latest operations on agent checkpoints.
package checkpoint

import (
	"context"
	"fmt"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// InMemoryStore is a test implementation of core.CheckpointStore.
// TODO(AE-4-deploy): implement PGCheckpointStore with pgxpool (UPSERT + Latest by step_index DESC).
type InMemoryStore struct {
	mu          sync.Mutex
	checkpoints map[string]*core.Checkpoint            // id → checkpoint
	sessions    map[string][]*core.Checkpoint           // session_id → ordered checkpoints
	maxPerSession int
}

// NewInMemoryStore creates an in-memory checkpoint store.
func NewInMemoryStore(maxPerSession int) *InMemoryStore {
	if maxPerSession <= 0 {
		maxPerSession = 20
	}
	return &InMemoryStore{
		checkpoints:   make(map[string]*core.Checkpoint),
		sessions:      make(map[string][]*core.Checkpoint),
		maxPerSession: maxPerSession,
	}
}

// Save persists a checkpoint. If a checkpoint with the same session_id+step_index
// exists, it is overwritten.
func (s *InMemoryStore) Save(ctx context.Context, cp *core.Checkpoint) error {
	if cp.ID == "" {
		return fmt.Errorf("checkpoint ID is required")
	}
	if cp.SessionID == "" {
		return fmt.Errorf("checkpoint session_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkpoints[cp.ID] = cp

	// Upsert into session list.
	list := s.sessions[cp.SessionID]
	found := false
	for i, existing := range list {
		if existing.StepIndex == cp.StepIndex {
			list[i] = cp
			found = true
			break
		}
	}
	if !found {
		list = append(list, cp)
	}

	// Enforce max per session (keep most recent).
	if len(list) > s.maxPerSession {
		list = list[len(list)-s.maxPerSession:]
	}
	s.sessions[cp.SessionID] = list

	return nil
}

// Load retrieves a checkpoint by ID.
func (s *InMemoryStore) Load(ctx context.Context, id string) (*core.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp, ok := s.checkpoints[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %q not found", id)
	}
	return cp, nil
}

// Latest returns the most recent checkpoint for a session (highest step_index).
func (s *InMemoryStore) Latest(ctx context.Context, sessionID string) (*core.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.sessions[sessionID]
	if len(list) == 0 {
		return nil, fmt.Errorf("no checkpoints for session %q", sessionID)
	}

	// Find highest step_index.
	latest := list[0]
	for _, cp := range list[1:] {
		if cp.StepIndex > latest.StepIndex {
			latest = cp
		}
	}
	return latest, nil
}

// Verify interface compliance.
var _ core.CheckpointStore = (*InMemoryStore)(nil)
