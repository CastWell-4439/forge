package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

func TestRecorderWritesEventsToolErrorsAndDecision(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".forgex")
	store := storage.NewFileStore(root)
	layout := store.Layout()
	runID := "run_trace_001"

	run := model.Run{
		ID:        runID,
		TaskID:    "task_trace_001",
		Name:      "trace recorder test",
		Status:    model.RunRunning,
		StartedAt: time.Now().UTC(),
	}
	packet := model.TaskPacket{
		ID:     "task_trace_001",
		Name:   "trace recorder test",
		Goal:   "record a failed tool call",
		Inputs: map[string]any{"material_id": 121503},
	}
	if err := store.InitRun(ctx, run, packet); err != nil {
		t.Fatalf("InitRun: %v", err)
	}

	recorder := NewRecorder(store, runID)
	if err := recorder.Event(ctx, model.EventRunStarted, "run started", nil); err != nil {
		t.Fatalf("Event: %v", err)
	}

	callID, err := recorder.ToolCallStarted(ctx, "vidu.reference2video", map[string]any{"images_refs": []any{}})
	if err != nil {
		t.Fatalf("ToolCallStarted: %v", err)
	}
	if callID == "" {
		t.Fatal("ToolCallStarted returned empty callID")
	}

	if err := recorder.ToolCallFailed(ctx, callID, errors.New("images_refs is empty")); err != nil {
		t.Fatalf("ToolCallFailed: %v", err)
	}

	if err := recorder.Error(ctx, model.ErrorEnvelope{
		Source:    "tool",
		Operation: "vidu.reference2video",
		Message:   "images_refs is empty",
	}); err != nil {
		t.Fatalf("Error: %v", err)
	}

	if err := recorder.StopDecision(ctx, model.StopDecision{
		Action: model.StopActionStop,
		Reason: "tool contract violation",
	}); err != nil {
		t.Fatalf("StopDecision: %v", err)
	}

	assertJSONLLinesAtLeast(t, layout.EventsFile(runID), 4)
	assertJSONLLinesAtLeast(t, layout.ToolCallsFile(runID), 2)
	assertJSONLLinesAtLeast(t, layout.ErrorsFile(runID), 1)
	assertJSONLLinesAtLeast(t, layout.StopDecisionsFile(runID), 1)
}

func TestRecorderSuccessfulToolCall(t *testing.T) {
	ctx := context.Background()
	store := storage.NewFileStore(filepath.Join(t.TempDir(), ".forgex"))
	runID := "run_trace_success"

	if err := store.InitRun(ctx, model.Run{ID: runID, TaskID: "task"}, model.TaskPacket{ID: "task"}); err != nil {
		t.Fatalf("InitRun: %v", err)
	}

	recorder := NewRecorder(store, runID)
	callID, err := recorder.ToolCallStarted(ctx, "material.detail", map[string]any{"material_id": 121503})
	if err != nil {
		t.Fatalf("ToolCallStarted: %v", err)
	}
	if err := recorder.ToolCallFinished(ctx, callID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("ToolCallFinished: %v", err)
	}

	assertJSONLLinesAtLeast(t, store.Layout().ToolCallsFile(runID), 2)
	assertJSONLLinesAtLeast(t, store.Layout().EventsFile(runID), 2)
}

func TestRecorderReturnsErrorForUnknownToolCall(t *testing.T) {
	ctx := context.Background()
	store := storage.NewFileStore(filepath.Join(t.TempDir(), ".forgex"))
	recorder := NewRecorder(store, "run_unknown")

	if err := recorder.ToolCallFinished(ctx, "missing", map[string]any{"ok": true}); err == nil {
		t.Fatal("expected error for missing tool call")
	}
	if err := recorder.ToolCallFailed(ctx, "missing", errors.New("failed")); err == nil {
		t.Fatal("expected error for missing failed tool call")
	}
}

func TestRecorderHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := storage.NewFileStore(filepath.Join(t.TempDir(), ".forgex"))
	recorder := NewRecorder(store, "run_cancelled")

	if err := recorder.Event(ctx, model.EventRunStarted, "run started", nil); err == nil {
		t.Fatal("expected cancelled context error")
	}
	if _, err := recorder.ToolCallStarted(ctx, "tool", nil); err == nil {
		t.Fatal("expected cancelled context error")
	}
}

func assertJSONLLinesAtLeast(t *testing.T, path string, wantMin int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open JSONL file %s: %v", path, err)
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		count++
		var decoded map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", count, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	if count < wantMin {
		t.Fatalf("line count = %d, want at least %d", count, wantMin)
	}
}
