package context

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

func TestLoadRunContextAndFormatInspect(t *testing.T) {
	root := t.TempDir()
	runID := "run_context_test"
	store := storage.NewFileStore(root)
	now := time.Now().UTC()
	if err := store.InitRun(context.Background(), model.Run{ID: runID, TaskID: "task_1", Name: "context demo", StartedAt: now}, model.TaskPacket{ID: "task_1", Name: "context demo"}); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}
	ledger := model.ProgressLedger{
		RunID:        runID,
		CurrentPhase: "phase_1",
		Checklist: []model.ProgressItem{
			{ID: "a", Title: "first", Status: model.ProgressDone},
			{ID: "b", Title: "second", Status: model.ProgressTodo},
		},
		NextActions: []string{"continue"},
		UpdatedAt:   now,
	}
	if err := store.SaveProgressLedger(context.Background(), ledger); err != nil {
		t.Fatalf("SaveProgressLedger() error = %v", err)
	}
	pack := NewBudgetManager(16).Build(runID, "inspect", "hello context", []string{"artifact_1"})
	if err := store.AppendContextPack(context.Background(), pack); err != nil {
		t.Fatalf("AppendContextPack() error = %v", err)
	}

	state, err := LoadRunContext(filepath.Join(root, "runs", runID))
	if err != nil {
		t.Fatalf("LoadRunContext() error = %v", err)
	}
	if state.Ledger == nil || state.Ledger.CurrentPhase != "phase_1" {
		t.Fatalf("ledger mismatch: %+v", state.Ledger)
	}
	if len(state.ContextPacks) != 1 || state.ContextPacks[0].Purpose != "inspect" {
		t.Fatalf("context packs mismatch: %+v", state.ContextPacks)
	}

	out := FormatInspect(state)
	for _, want := range []string{"ForgeX Context Inspect", "phase_1", "completion: 50%", "purpose=inspect", "artifact_1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("inspect output missing %q\n%s", want, out)
		}
	}
}
