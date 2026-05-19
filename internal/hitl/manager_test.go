package hitl

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- In-memory store for testing ---

type memStore struct {
	mu   sync.RWMutex
	data map[string]*Request
}

func newMemStore() *memStore {
	return &memStore{data: make(map[string]*Request)}
}

func (s *memStore) Save(_ context.Context, req *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[req.ID] = req
	return nil
}

func (s *memStore) Get(_ context.Context, id string) (*Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.data[id]; ok {
		return r, nil
	}
	return nil, nil
}

func (s *memStore) ListPending(_ context.Context) ([]*Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Request
	for _, r := range s.data {
		if r.Status == StatusPending {
			result = append(result, r)
		}
	}
	return result, nil
}

func (s *memStore) Update(_ context.Context, req *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[req.ID] = req
	return nil
}

// --- Manager Tests ---

func TestManager_CreateAndRespond(t *testing.T) {
	store := newMemStore()
	var notified bool

	mgr := NewManager(ManagerConfig{
		Store: store,
		Callback: func(ctx context.Context, req *Request) error {
			notified = true
			return nil
		},
		Timeout: time.Hour,
	})

	ctx := context.Background()

	req := &Request{
		ID:         "hitl-1",
		WorkflowID: "wf-123",
		TaskID:     "task-1",
		Message:    "Approve this MR?",
		Options:    []string{"approve", "reject"},
	}

	// Create
	if err := mgr.Create(ctx, req); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !notified {
		t.Error("callback was not called")
	}
	if mgr.PendingCount() != 1 {
		t.Errorf("pending = %d, want 1", mgr.PendingCount())
	}

	// Verify stored
	saved, _ := store.Get(ctx, "hitl-1")
	if saved == nil || saved.Status != StatusPending {
		t.Fatal("request not saved correctly")
	}

	// Respond
	err := mgr.Respond(ctx, "hitl-1", &Response{Decision: "approve", Feedback: "LGTM"})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if mgr.PendingCount() != 0 {
		t.Errorf("pending after respond = %d", mgr.PendingCount())
	}

	// Verify updated
	updated, _ := store.Get(ctx, "hitl-1")
	if updated.Status != StatusResponded {
		t.Errorf("status = %s, want responded", updated.Status)
	}
	if updated.Response.Decision != "approve" {
		t.Errorf("decision = %s", updated.Response.Decision)
	}
}

func TestManager_RespondNotFound(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		Store:   newMemStore(),
		Timeout: time.Hour,
	})
	err := mgr.Respond(context.Background(), "nonexistent", &Response{Decision: "ok"})
	if err == nil {
		t.Fatal("expected error for non-existent request")
	}
}

func TestManager_CheckTimeouts(t *testing.T) {
	store := newMemStore()
	mgr := NewManager(ManagerConfig{
		Store:   store,
		Timeout: 10 * time.Millisecond,
	})

	ctx := context.Background()
	req := &Request{
		ID:         "hitl-timeout",
		WorkflowID: "wf-1",
		TaskID:     "task-1",
		Message:    "Will timeout",
		Options:    []string{"yes"},
	}
	mgr.Create(ctx, req)

	time.Sleep(20 * time.Millisecond)
	count := mgr.CheckTimeouts(ctx)
	if count != 1 {
		t.Errorf("timed out count = %d, want 1", count)
	}
	if mgr.PendingCount() != 0 {
		t.Errorf("pending after timeout = %d", mgr.PendingCount())
	}

	updated, _ := store.Get(ctx, "hitl-timeout")
	if updated.Status != StatusTimeout {
		t.Errorf("status = %s, want timeout", updated.Status)
	}
}

func TestManager_Recover(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()

	// Simulate pre-existing pending request in store
	store.Save(ctx, &Request{
		ID:         "hitl-old",
		WorkflowID: "wf-1",
		TaskID:     "t1",
		Status:     StatusPending,
		TimeoutAt:  time.Now().Add(time.Hour),
	})

	mgr := NewManager(ManagerConfig{
		Store:   store,
		Timeout: time.Hour,
	})

	if err := mgr.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if mgr.PendingCount() != 1 {
		t.Errorf("pending after recover = %d, want 1", mgr.PendingCount())
	}
}

func TestManager_CreateValidation(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	ctx := context.Background()

	// Missing ID
	err := mgr.Create(ctx, &Request{WorkflowID: "wf"})
	if err == nil {
		t.Error("expected error for missing ID")
	}

	// Missing WorkflowID
	err = mgr.Create(ctx, &Request{ID: "x"})
	if err == nil {
		t.Error("expected error for missing workflow ID")
	}
}

// --- WorkflowAPI Tests ---

func TestWorkflowAPI_ResumeHITL(t *testing.T) {
	store := newMemStore()
	mgr := NewManager(ManagerConfig{Store: store, Timeout: time.Hour})
	ctx := context.Background()

	// Create a pending request
	mgr.Create(ctx, &Request{
		ID: "api-hitl-1", WorkflowID: "wf-1", TaskID: "t1",
		Message: "test", Options: []string{"go"},
	})

	api := NewWorkflowAPI(WorkflowAPIConfig{HITLManager: mgr})
	if err := api.ResumeHITL(ctx, "api-hitl-1", "go", "all good"); err != nil {
		t.Fatalf("ResumeHITL: %v", err)
	}

	req, _ := store.Get(ctx, "api-hitl-1")
	if req.Status != StatusResponded {
		t.Errorf("status = %s", req.Status)
	}
}

func TestWorkflowAPI_TriggerWorkflow(t *testing.T) {
	api := NewWorkflowAPI(WorkflowAPIConfig{
		HITLManager: NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour}),
		TriggerFn: func(ctx context.Context, name string, inputs map[string]string) (string, error) {
			return "instance-42", nil
		},
	})

	id, err := api.TriggerWorkflow(context.Background(), "bug_fix", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "instance-42" {
		t.Errorf("instance id = %s", id)
	}
}
