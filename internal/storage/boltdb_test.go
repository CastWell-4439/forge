package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// newTestBolt creates a fresh BoltStorage in a temp dir for unit tests.
func newTestBolt(t *testing.T) *BoltStorage {
	t.Helper()
	dir := t.TempDir()
	store, err := NewBoltStorage(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewBoltStorage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// saveRunningTask inserts a task in RUNNING state for the tests below.
func saveRunningTask(t *testing.T, store *BoltStorage, taskID string) *Task {
	t.Helper()
	now := time.Now()
	task := &Task{
		ID:         taskID,
		WorkflowID: "wf-test",
		TaskName:   "test-task",
		Handler:    "test.handler",
		Status:     TaskStatusRunning,
		Input:      json.RawMessage(`{}`),
		StartedAt:  &now,
		CreatedAt:  now,
	}
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	return task
}

func TestCompleteTask_PersistsOutput(t *testing.T) {
	store := newTestBolt(t)
	saveRunningTask(t, store, "task-1")

	output := json.RawMessage(`{"language":"go","value":42}`)
	if err := store.CompleteTask(context.Background(), "task-1", output); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Errorf("status = %q, want COMPLETED", got.Status)
	}
	if string(got.Output) != string(output) {
		t.Errorf("output = %s, want %s", string(got.Output), string(output))
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should not be nil after completion")
	}
}

func TestFailTask_PersistsErrorMsg(t *testing.T) {
	store := newTestBolt(t)
	saveRunningTask(t, store, "task-2")

	if err := store.FailTask(context.Background(), "task-2", "boom"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-2")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != TaskStatusFailed {
		t.Errorf("status = %q, want FAILED", got.Status)
	}
	if got.ErrorMsg != "boom" {
		t.Errorf("error_msg = %q, want boom", got.ErrorMsg)
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should not be nil after failure")
	}
}

func TestCompleteTask_Idempotent(t *testing.T) {
	store := newTestBolt(t)
	saveRunningTask(t, store, "task-3")

	first := json.RawMessage(`{"first":true}`)
	if err := store.CompleteTask(context.Background(), "task-3", first); err != nil {
		t.Fatalf("first CompleteTask: %v", err)
	}

	// Second call must be a no-op and must NOT overwrite output.
	second := json.RawMessage(`{"second":true}`)
	if err := store.CompleteTask(context.Background(), "task-3", second); err != nil {
		t.Fatalf("second CompleteTask: %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-3")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if string(got.Output) != string(first) {
		t.Errorf("output = %s, want original %s (terminal guard should prevent overwrite)",
			string(got.Output), string(first))
	}
}

func TestFailTask_Idempotent(t *testing.T) {
	store := newTestBolt(t)
	saveRunningTask(t, store, "task-4")

	if err := store.FailTask(context.Background(), "task-4", "first-error"); err != nil {
		t.Fatalf("first FailTask: %v", err)
	}

	// Second call must not overwrite error_msg.
	if err := store.FailTask(context.Background(), "task-4", "second-error"); err != nil {
		t.Fatalf("second FailTask: %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-4")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ErrorMsg != "first-error" {
		t.Errorf("error_msg = %q, want first-error (terminal guard should prevent overwrite)",
			got.ErrorMsg)
	}
}

func TestCompleteTask_NotFound(t *testing.T) {
	store := newTestBolt(t)

	// Must not error, just log a warning and no-op.
	err := store.CompleteTask(context.Background(), "does-not-exist", json.RawMessage(`{}`))
	if err != nil {
		t.Errorf("CompleteTask on missing task returned err = %v, want nil", err)
	}
}

// TestCompleteTask_RejectFromFailed: once a task is FAILED, calling CompleteTask
// must not flip it to COMPLETED or write output.
func TestCompleteTask_RejectFromFailed(t *testing.T) {
	store := newTestBolt(t)
	saveRunningTask(t, store, "task-5")

	if err := store.FailTask(context.Background(), "task-5", "upstream-error"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	// Now attempt to complete it — should be rejected (no-op).
	if err := store.CompleteTask(context.Background(), "task-5",
		json.RawMessage(`{"late":"result"}`)); err != nil {
		t.Fatalf("CompleteTask (late): %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-5")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != TaskStatusFailed {
		t.Errorf("status = %q, want FAILED (terminal guard)", got.Status)
	}
	if got.ErrorMsg != "upstream-error" {
		t.Errorf("error_msg = %q, want upstream-error", got.ErrorMsg)
	}
	// Output should remain whatever it was pre-fail (unset → null / empty).
	// Key assertion: it must NOT equal the late `{"late":"result"}`.
	if s := string(got.Output); s == `{"late":"result"}` {
		t.Errorf("output = %s, late CompleteTask should not have overwritten it", s)
	}
}
