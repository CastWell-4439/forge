// Package hitl implements the Human-In-The-Loop manager for Forge workflows.
// It handles pause requests, user responses, and timeout enforcement.
package hitl

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RequestStatus represents the state of a HITL request.
type RequestStatus string

const (
	StatusPending   RequestStatus = "pending"
	StatusResponded RequestStatus = "responded"
	StatusTimeout   RequestStatus = "timeout"
)

// Request represents a pending human decision.
type Request struct {
	ID         string
	WorkflowID string
	TaskID     string
	Message    string
	Options    []string      // e.g. ["approve", "reject", "modify"]
	Status     RequestStatus
	Response   *Response
	CreatedAt  time.Time
	TimeoutAt  time.Time
}

// Response holds the human's decision.
type Response struct {
	Decision string // one of the Options
	Feedback string // optional free-text
}

// HITLCallback is called when a HITL request is created (to notify external systems).
type HITLCallback func(ctx context.Context, req *Request) error

// Store persists HITL requests (for crash recovery).
type Store interface {
	Save(ctx context.Context, req *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	ListPending(ctx context.Context) ([]*Request, error)
	Update(ctx context.Context, req *Request) error
}

// Manager orchestrates HITL interactions.
type Manager struct {
	mu       sync.RWMutex
	pending  map[string]*Request
	store    Store
	callback HITLCallback
	timeout  time.Duration // default timeout for requests
}

// ManagerConfig configures the HITL Manager.
type ManagerConfig struct {
	Store    Store
	Callback HITLCallback
	Timeout  time.Duration // default request timeout
}

// NewManager creates a new HITL Manager.
func NewManager(cfg ManagerConfig) *Manager {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 24 * time.Hour
	}
	return &Manager{
		pending:  make(map[string]*Request),
		store:    cfg.Store,
		callback: cfg.Callback,
		timeout:  timeout,
	}
}

// Create registers a new HITL request and invokes the callback to notify the user.
func (m *Manager) Create(ctx context.Context, req *Request) error {
	if req.ID == "" {
		return fmt.Errorf("hitl: request ID is required")
	}
	if req.WorkflowID == "" {
		return fmt.Errorf("hitl: workflow ID is required")
	}

	req.Status = StatusPending
	req.CreatedAt = time.Now()
	if req.TimeoutAt.IsZero() {
		req.TimeoutAt = req.CreatedAt.Add(m.timeout)
	}

	m.mu.Lock()
	m.pending[req.ID] = req
	m.mu.Unlock()

	// Persist
	if m.store != nil {
		if err := m.store.Save(ctx, req); err != nil {
			return fmt.Errorf("hitl: save request: %w", err)
		}
	}

	// Notify external system (e.g. OpenClaw → Feishu message)
	if m.callback != nil {
		if err := m.callback(ctx, req); err != nil {
			return fmt.Errorf("hitl: callback: %w", err)
		}
	}

	return nil
}

// Respond records a human's response to a HITL request.
func (m *Manager) Respond(ctx context.Context, requestID string, resp *Response) error {
	m.mu.Lock()
	req, exists := m.pending[requestID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("hitl: request %q not found or already resolved", requestID)
	}
	if req.Status != StatusPending {
		m.mu.Unlock()
		return fmt.Errorf("hitl: request %q is %s, cannot respond", requestID, req.Status)
	}

	req.Status = StatusResponded
	req.Response = resp
	delete(m.pending, requestID)
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.Update(ctx, req); err != nil {
			return fmt.Errorf("hitl: update request: %w", err)
		}
	}

	return nil
}

// Get retrieves a request by ID (from memory or store).
func (m *Manager) Get(ctx context.Context, id string) (*Request, error) {
	m.mu.RLock()
	if req, ok := m.pending[id]; ok {
		m.mu.RUnlock()
		return req, nil
	}
	m.mu.RUnlock()

	if m.store != nil {
		return m.store.Get(ctx, id)
	}
	return nil, fmt.Errorf("hitl: request %q not found", id)
}

// CheckTimeouts marks expired pending requests as timed out.
func (m *Manager) CheckTimeouts(ctx context.Context) int {
	now := time.Now()
	m.mu.Lock()
	var timedOut []*Request
	for id, req := range m.pending {
		if now.After(req.TimeoutAt) {
			req.Status = StatusTimeout
			timedOut = append(timedOut, req)
			delete(m.pending, id)
		}
	}
	m.mu.Unlock()

	// Persist timeout status
	for _, req := range timedOut {
		if m.store != nil {
			m.store.Update(ctx, req)
		}
	}
	return len(timedOut)
}

// PendingCount returns the number of pending requests.
func (m *Manager) PendingCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pending)
}

// Recover loads pending requests from store on startup.
func (m *Manager) Recover(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	reqs, err := m.store.ListPending(ctx)
	if err != nil {
		return fmt.Errorf("hitl: recover: %w", err)
	}
	m.mu.Lock()
	for _, req := range reqs {
		m.pending[req.ID] = req
	}
	m.mu.Unlock()
	return nil
}
