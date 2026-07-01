package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/metrics"
	"github.com/castwell/forge/internal/forgex/model"
)

func TestSQLiteIndexRunDir(t *testing.T) {
	root := t.TempDir()
	runID := "run_index_test"
	store := NewFileStore(root)
	packet := model.TaskPacket{ID: "task_index", Name: "index demo", Goal: "index local run"}
	run := model.Run{ID: runID, TaskID: packet.ID, Name: packet.Name, Status: model.RunStopped, StartedAt: time.Now().UTC()}
	if err := store.InitRun(context.Background(), run, packet); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}
	if err := store.AppendError(context.Background(), model.ErrorEnvelope{
		ID:          "err_1",
		RunID:       runID,
		Operation:   "vidu.reference2video",
		Message:     "images_refs is empty",
		Category:    "tool_contract_violation",
		Severity:    "high",
		Fingerprint: "fp_1",
		Timestamp:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendError() error = %v", err)
	}
	if err := store.AppendStopDecision(context.Background(), model.StopDecision{
		ID:        "decision_1",
		RunID:     runID,
		ErrorID:   "err_1",
		Action:    model.StopActionStop,
		Reason:    "contract violation",
		DecidedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendStopDecision() error = %v", err)
	}

	idx, err := OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex() error = %v", err)
	}
	defer idx.Close()

	if err := idx.IndexRunDir(context.Background(), filepath.Join(root, "runs", runID)); err != nil {
		t.Fatalf("IndexRunDir() error = %v", err)
	}

	runs, err := idx.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() len = %d", len(runs))
	}
	got := runs[0]
	if got.ID != runID || got.ErrorCount != 1 || got.StopAction != "stop" || got.LastCategory != "tool_contract_violation" || got.LastFingerprint != "fp_1" {
		t.Fatalf("indexed run mismatch: %+v", got)
	}
}

func TestSQLiteIndexControlMetrics(t *testing.T) {
	root := t.TempDir()
	runID := "run_metrics_test"
	store := NewFileStore(root)
	packet := model.TaskPacket{ID: "task_metrics", Name: "metrics demo", Goal: "index control metrics"}
	run := model.Run{ID: runID, TaskID: packet.ID, Name: packet.Name, Status: model.RunStopped, StartedAt: time.Now().UTC()}
	if err := store.InitRun(context.Background(), run, packet); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}
	if err := store.AppendPolicyDecision(context.Background(), model.PolicyDecision{ID: "pd_1", RunID: runID, ToolName: "vidu.reference2video", Action: "require_approval", RequiresHITL: true}); err != nil {
		t.Fatalf("AppendPolicyDecision() error = %v", err)
	}
	if err := store.AppendContractValidation(context.Background(), model.ContractValidation{ID: "cv_1", RunID: runID, ToolName: "vidu.reference2video", Status: "failed", Message: "images_refs is empty"}); err != nil {
		t.Fatalf("AppendContractValidation() error = %v", err)
	}
	if err := store.AppendArtifact(context.Background(), model.ArtifactRecord{ID: "art_1", RunID: runID, Type: "reference_image", Status: model.ArtifactMissing}); err != nil {
		t.Fatalf("AppendArtifact() error = %v", err)
	}
	if err := store.AppendStopDecision(context.Background(), model.StopDecision{ID: "sd_1", RunID: runID, Action: model.StopActionStop, Reason: "contract violation", DecidedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AppendStopDecision() error = %v", err)
	}
	if err := store.SaveWorldState(context.Background(), model.WorldState{RunID: runID, Version: 1, Entries: []model.StateEntry{{Key: "reference_images.status", Status: model.StateConflicted}}}); err != nil {
		t.Fatalf("SaveWorldState() error = %v", err)
	}

	idx, err := OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex() error = %v", err)
	}
	defer idx.Close()
	if err := idx.IndexRunDir(context.Background(), filepath.Join(root, "runs", runID)); err != nil {
		t.Fatalf("IndexRunDir() error = %v", err)
	}
	runs, err := idx.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() len = %d", len(runs))
	}
	m := runs[0].Metrics
	if m.PolicyDecisionCount != 1 || m.ApprovalRequiredCount != 1 {
		t.Errorf("policy metrics mismatch: %+v", m)
	}
	if m.ContractValidationFailedCount != 1 || m.MissingArtifactCount != 1 {
		t.Errorf("validation/artifact metrics mismatch: %+v", m)
	}
	if m.SafeStopCount != 1 || m.StateConflictCount != 1 {
		t.Errorf("stop/conflict metrics mismatch: %+v", m)
	}
}

// TestSQLiteIndexMissingOptionalFiles verifies that a run directory containing
// only run.json indexes without error and yields zero control metrics.
func TestSQLiteIndexMissingOptionalFiles(t *testing.T) {
	root := t.TempDir()
	runID := "run_minimal"
	store := NewFileStore(root)
	packet := model.TaskPacket{ID: "task_minimal", Name: "minimal", Goal: "minimal"}
	run := model.Run{ID: runID, TaskID: packet.ID, Name: packet.Name, Status: model.RunSucceeded, StartedAt: time.Now().UTC()}
	if err := store.InitRun(context.Background(), run, packet); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}

	idx, err := OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex() error = %v", err)
	}
	defer idx.Close()
	if err := idx.IndexRunDir(context.Background(), filepath.Join(root, "runs", runID)); err != nil {
		t.Fatalf("IndexRunDir() error = %v", err)
	}
	runs, err := idx.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() len = %d", len(runs))
	}
	if (runs[0].Metrics != metrics.ControlMetrics{}) {
		t.Fatalf("expected zero metrics, got %+v", runs[0].Metrics)
	}
}

func TestSQLiteIndexUpsert(t *testing.T) {
	root := t.TempDir()
	idx, err := OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex() error = %v", err)
	}
	defer idx.Close()

	run := model.Run{ID: "run_upsert", TaskID: "task_1", Name: "first", Status: model.RunRunning, StartedAt: time.Now().UTC()}
	artifacts := indexArtifacts{Run: run}
	if err := idx.IndexArtifacts(context.Background(), artifacts); err != nil {
		t.Fatalf("IndexArtifacts() error = %v", err)
	}
	run.Name = "second"
	run.Status = model.RunStopped
	artifacts.Run = run
	artifacts.StopDecisions = []model.StopDecision{{ID: "decision_1", RunID: run.ID, Action: model.StopActionStop, DecidedAt: time.Now().UTC()}}
	if err := idx.IndexArtifacts(context.Background(), artifacts); err != nil {
		t.Fatalf("IndexArtifacts() upsert error = %v", err)
	}
	runs, err := idx.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() len = %d", len(runs))
	}
	if runs[0].Name != "second" || runs[0].Status != "stopped" || runs[0].StopAction != "stop" {
		t.Fatalf("upsert mismatch: %+v", runs[0])
	}
}
