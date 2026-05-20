package hitlworker

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/castwell/forge/internal/hitl"
)

func testManager() *hitl.Manager {
	return hitl.NewManager(hitl.ManagerConfig{
		Callback: func(ctx context.Context, req *hitl.Request) error {
			return nil // no-op callback for tests
		},
	})
}

func testIDGen() func() string {
	var counter atomic.Int64
	return func() string {
		return "test_" + strings.Repeat("0", 5) + string(rune('0'+counter.Add(1)))
	}
}

func TestNotify(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_001" })

	result, err := w.Execute(context.Background(), "notify", map[string]any{
		"message":     "Build completed successfully",
		"workflow_id": "wf_1",
		"task_id":     "task_1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res map[string]any
	json.Unmarshal([]byte(result), &res)
	if res["status"] != "sent" {
		t.Errorf("expected status=sent, got %v", res["status"])
	}
	if res["request_id"] != "req_001" {
		t.Errorf("expected request_id=req_001, got %v", res["request_id"])
	}
}

func TestNotify_MissingMessage(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, nil)

	_, err := w.Execute(context.Background(), "notify", map[string]any{"workflow_id": "wf_x"})
	if err == nil || !strings.Contains(err.Error(), "'message' parameter required") {
		t.Errorf("expected message required error, got: %v", err)
	}
}

func TestNotify_MissingWorkflowID(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, nil)

	_, err := w.Execute(context.Background(), "notify", map[string]any{"message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "'workflow_id' parameter required") {
		t.Errorf("expected workflow_id required error, got: %v", err)
	}
}

func TestRequestApproval(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_002" })

	result, err := w.Execute(context.Background(), "request_approval", map[string]any{
		"message":     "Deploy to production?",
		"workflow_id": "wf_1",
		"task_id":     "task_2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res map[string]any
	json.Unmarshal([]byte(result), &res)
	if res["status"] != "pending" {
		t.Errorf("expected status=pending, got %v", res["status"])
	}

	// Verify request exists in manager
	req, err := mgr.Get(context.Background(), "req_002")
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	if req.Message != "Deploy to production?" {
		t.Errorf("message mismatch: %s", req.Message)
	}
	if len(req.Options) != 2 || req.Options[0] != "approve" {
		t.Errorf("options mismatch: %v", req.Options)
	}
}

func TestRequestApproval_CustomOptions(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_003" })

	_, err := w.Execute(context.Background(), "request_approval", map[string]any{
		"message":     "Choose action",
		"workflow_id": "wf_1",
		"options":     []any{"deploy", "rollback", "skip"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, _ := mgr.Get(context.Background(), "req_003")
	if len(req.Options) != 3 || req.Options[1] != "rollback" {
		t.Errorf("custom options not applied: %v", req.Options)
	}
}

func TestRequestInput(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_004" })

	result, err := w.Execute(context.Background(), "request_input", map[string]any{
		"message":         "Please provide the hotfix commit SHA:",
		"workflow_id":     "wf_2",
		"task_id":         "task_3",
		"timeout_minutes": float64(30),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res map[string]any
	json.Unmarshal([]byte(result), &res)
	if res["status"] != "pending" {
		t.Errorf("expected pending, got %v", res["status"])
	}
	if !strings.Contains(res["timeout"].(string), "30m") {
		t.Errorf("expected 30m timeout, got %v", res["timeout"])
	}
}

func TestNotifyAndWait(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_005" })

	result, err := w.Execute(context.Background(), "notify_and_wait", map[string]any{
		"message":     "Review ready. Acknowledge when done.",
		"workflow_id": "wf_3",
		"timeout":    "1h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res map[string]any
	json.Unmarshal([]byte(result), &res)
	if res["status"] != "pending" {
		t.Errorf("expected pending, got %v", res["status"])
	}

	req, _ := mgr.Get(context.Background(), "req_005")
	if req.Options[0] != "ack" {
		t.Errorf("expected ack option, got %v", req.Options)
	}
}

func TestUnknownAction(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, nil)

	_, err := w.Execute(context.Background(), "shutdown", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected unknown action error, got: %v", err)
	}
}

func TestRequestApproval_Response(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_006" })

	// Create approval request
	_, err := w.Execute(context.Background(), "request_approval", map[string]any{
		"message":     "Proceed?",
		"workflow_id": "wf_4",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify request is pending before response
	req, err := mgr.Get(context.Background(), "req_006")
	if err != nil {
		t.Fatalf("get before respond: %v", err)
	}
	if req.Status != hitl.StatusPending {
		t.Errorf("expected pending before respond, got %s", req.Status)
	}

	// Simulate human response
	err = mgr.Respond(context.Background(), "req_006", &hitl.Response{
		Decision: "approve",
		Feedback: "LGTM",
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}

	// After Respond, manager removes from pending (no store configured),
	// so we verify the Respond itself succeeded (no error above).
	// The pending count should be 0.
	if mgr.PendingCount() != 0 {
		t.Errorf("expected 0 pending after respond, got %d", mgr.PendingCount())
	}
}

func TestDefaultTimeout(t *testing.T) {
	mgr := testManager()
	w := NewWorker(mgr, func() string { return "req_007" })

	_, err := w.Execute(context.Background(), "request_input", map[string]any{
		"message":     "Enter value:",
		"workflow_id": "wf_5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req, _ := mgr.Get(context.Background(), "req_007")
	// Default timeout should be ~24h from now
	if req.TimeoutAt.IsZero() {
		t.Error("timeout should not be zero")
	}
}
