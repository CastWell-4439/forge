package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// requirePG connects to the CI-provided PostgreSQL and ensures the schema.
// Locally, without FORGE_PG_DSN, every test in this file skips — the same
// env gate the database worker's integration tests use.
func requirePG(t *testing.T) *PGStorage {
	t.Helper()
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	ctx := context.Background()
	store, err := NewPGStorage(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPGStorage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	sql, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrations", "001_init.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	// Idempotent (IF NOT EXISTS throughout), so re-running per test is fine.
	if err := store.RunMigrations(ctx, string(sql)); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	return store
}

// The full Storage contract against a real database: definitions, workflow
// lifecycle, the SKIP LOCKED claim, idempotent completion guards, and the
// event log. Every one of these was implemented but never executed before
// this wiring round — the test exists so "implemented" and "verified" are
// no longer the same claim.
func TestPGStorageFullContract(t *testing.T) {
	store := requirePG(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	// --- Definition ---
	def := &WorkflowDefinition{
		Name:    "pgtest-" + suffix,
		Version: 1,
		DagYAML: json.RawMessage(`{"name":"pgtest"}`),
	}
	if err := store.SaveWorkflowDefinition(ctx, def); err != nil {
		t.Fatalf("SaveWorkflowDefinition: %v", err)
	}
	if def.ID == 0 {
		t.Fatal("definition id not populated")
	}
	gotDef, err := store.GetWorkflowDefinition(ctx, def.Name, 1)
	if err != nil {
		t.Fatalf("GetWorkflowDefinition: %v", err)
	}
	if gotDef.Name != def.Name {
		t.Errorf("definition name = %q, want %q", gotDef.Name, def.Name)
	}

	// --- Workflow lifecycle ---
	wf := &Workflow{
		ID:        "wf-" + suffix,
		DefID:     def.ID,
		Name:      def.Name,
		Status:    WorkflowStatusPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveWorkflow(ctx, wf); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}
	gotWF, err := store.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if gotWF.Status != WorkflowStatusPending {
		t.Errorf("status = %s, want PENDING", gotWF.Status)
	}

	if err := store.UpdateWorkflowStatus(ctx, wf.ID, WorkflowStatusRunning); err != nil {
		t.Fatalf("UpdateWorkflowStatus(running): %v", err)
	}
	if err := store.UpdateWorkflowStatus(ctx, wf.ID, WorkflowStatusCompleted); err != nil {
		t.Fatalf("UpdateWorkflowStatus(completed): %v", err)
	}
	gotWF, err = store.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow after update: %v", err)
	}
	if gotWF.Status != WorkflowStatusCompleted || gotWF.FinishedAt == nil {
		t.Errorf("status=%s finished_at=%v, want COMPLETED with a finish time", gotWF.Status, gotWF.FinishedAt)
	}

	// --- Task + claim ---
	// A unique handler keeps ClaimTask's handler = ANY(...) matching only
	// this test's rows even on a shared database.
	handler := "pg-claim-" + suffix
	task := &Task{
		ID:          "task-" + suffix,
		WorkflowID:  wf.ID,
		TaskName:    "claim-me",
		Handler:     handler,
		Status:      TaskStatusReady,
		MaxAttempts: 1,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if _, err := store.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	tasks, err := store.ListTasksByWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("ListTasksByWorkflow: %v", err)
	}
	found := false
	for _, tk := range tasks {
		if tk.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Error("saved task missing from ListTasksByWorkflow")
	}

	claimed, err := store.ClaimTask(ctx, "worker-pg-test", []string{handler})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed == nil || claimed.ID != task.ID {
		t.Fatalf("claimed = %+v, want task %s", claimed, task.ID)
	}
	if claimed.Status != TaskStatusScheduled || claimed.WorkerID != "worker-pg-test" {
		t.Errorf("claimed status=%s worker=%s, want SCHEDULED/worker-pg-test", claimed.Status, claimed.WorkerID)
	}
	// Nothing READY remains for this handler: the second claim comes back
	// empty rather than stealing another test's row.
	again, err := store.ClaimTask(ctx, "worker-pg-test", []string{handler})
	if err != nil {
		t.Fatalf("second ClaimTask: %v", err)
	}
	if again != nil {
		t.Errorf("second claim returned %+v, want nil", again)
	}

	// --- Terminal transition + idempotency guards ---
	if err := store.UpdateTaskStatus(ctx, task.ID, TaskStatusRunning); err != nil {
		t.Fatalf("UpdateTaskStatus(running): %v", err)
	}
	if err := store.CompleteTask(ctx, task.ID, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	// A repeat completion is a warning, not an error (0 rows by design).
	if err := store.CompleteTask(ctx, task.ID, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Errorf("CompleteTask must be idempotent, got: %v", err)
	}
	// A late failure cannot reopen a terminal task.
	if err := store.FailTask(ctx, task.ID, "too late"); err != nil {
		t.Errorf("FailTask on a terminal task must be a guarded no-op, got: %v", err)
	}
	final, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask final: %v", err)
	}
	if final.Status != TaskStatusCompleted {
		t.Errorf("status = %s after late FailTask, want COMPLETED", final.Status)
	}

	// --- Event log ---
	if err := store.SaveEvent(ctx, &Event{
		WorkflowID:  wf.ID,
		TaskID:      task.ID,
		Type:        EventTaskScheduled,
		Payload:     json.RawMessage(`{"seq":"one"}`),
		SequenceNum: 1,
	}); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
	history, err := store.GetWorkflowHistory(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflowHistory: %v", err)
	}
	if len(history) != 1 || history[0].SequenceNum != 1 || history[0].Type != EventTaskScheduled {
		t.Errorf("history = %+v, want the single scheduled event", history)
	}

	// --- Aggregation ---
	counts, err := store.CountWorkflows(ctx)
	if err != nil {
		t.Fatalf("CountWorkflows: %v", err)
	}
	if counts[WorkflowStatusCompleted] < 1 {
		t.Errorf("completed count = %d, want at least this run's workflow", counts[WorkflowStatusCompleted])
	}
}

// A missing migrations directory is a warning, not a failure: it only means
// the checkout was not found and the schema may already exist. This path
// never touches the database, so it runs without FORGE_PG_DSN.
func TestMigrateUpMissingDirIsNotAFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if err := MigrateUp(context.Background(), nil, missing); err != nil {
		t.Errorf("MigrateUp with a missing dir = %v, want nil (warning only)", err)
	}
}
