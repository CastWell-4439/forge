package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestFileStoreInitRunAndAppendStreams(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".forgex")
	store := NewFileStore(root)
	layout := store.Layout()
	now := time.Date(2026, 6, 26, 21, 0, 0, 0, time.UTC)

	run := model.Run{
		ID:        "run_001",
		TaskID:    "task_001",
		Name:      "AIhook demo",
		Status:    model.RunRunning,
		StartedAt: now,
	}
	packet := model.TaskPacket{
		ID:     "task_001",
		Name:   "AIhook empty images_refs demo",
		Goal:   "Generate AI Hook video with Vidu reference2video.",
		Inputs: map[string]any{"material_id": 121503},
	}

	if err := store.InitRun(ctx, run, packet); err != nil {
		t.Fatalf("InitRun: %v", err)
	}

	assertFileExists(t, layout.RunFile(run.ID))
	assertFileExists(t, layout.TaskPacketFile(run.ID))

	runBytes, err := os.ReadFile(layout.RunFile(run.ID))
	if err != nil {
		t.Fatalf("read run file: %v", err)
	}
	var decodedRun model.Run
	if err := json.Unmarshal(runBytes, &decodedRun); err != nil {
		t.Fatalf("run file should be JSON: %v", err)
	}
	if decodedRun.ID != run.ID || decodedRun.Status != model.RunRunning {
		t.Fatalf("unexpected run metadata: %+v", decodedRun)
	}

	if err := store.AppendEvent(ctx, model.Event{
		ID:        "evt_001",
		RunID:     run.ID,
		Type:      model.EventRunStarted,
		Message:   "started",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.AppendEvent(ctx, model.Event{
		ID:        "evt_002",
		RunID:     run.ID,
		Type:      model.EventToolFailed,
		Message:   "failed",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("AppendEvent second line: %v", err)
	}
	assertJSONLLines(t, layout.EventsFile(run.ID), 2)

	if err := store.AppendSpan(ctx, model.Span{
		ID:        "span_001",
		RunID:     run.ID,
		Name:      "vidu.reference2video",
		StartedAt: now,
		Status:    "failed",
	}); err != nil {
		t.Fatalf("AppendSpan: %v", err)
	}
	assertJSONLLines(t, layout.SpansFile(run.ID), 1)

	if err := store.AppendToolCall(ctx, model.ToolCall{
		ID:        "tool_001",
		RunID:     run.ID,
		ToolName:  "vidu.reference2video",
		Error:     "images_refs is empty",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("AppendToolCall: %v", err)
	}
	assertJSONLLines(t, layout.ToolCallsFile(run.ID), 1)

	if err := store.AppendError(ctx, model.ErrorEnvelope{
		ID:        "err_001",
		RunID:     run.ID,
		Source:    "tool",
		Operation: "vidu.reference2video",
		Message:   "images_refs is empty",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("AppendError: %v", err)
	}
	assertJSONLLines(t, layout.ErrorsFile(run.ID), 1)

	if err := store.AppendStopDecision(ctx, model.StopDecision{
		ID:        "decision_001",
		RunID:     run.ID,
		ErrorID:   "err_001",
		Action:    model.StopActionStop,
		Reason:    "tool contract violation",
		DecidedAt: now,
	}); err != nil {
		t.Fatalf("AppendStopDecision: %v", err)
	}
	assertJSONLLines(t, layout.StopDecisionsFile(run.ID), 1)

	if err := store.WriteReport(ctx, run.ID, "# ForgeX Run Report\n"); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	assertFileExists(t, layout.ReportFile(run.ID))

	if err := store.WriteBadCase(ctx, run.ID, []byte("id: AIHOOK_001\n")); err != nil {
		t.Fatalf("WriteBadCase: %v", err)
	}
	assertFileExists(t, layout.BadCaseFile(run.ID))
}

func TestAppendJSONLCreatesParentAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")

	if err := AppendJSONL(path, map[string]string{"id": "1"}); err != nil {
		t.Fatalf("AppendJSONL first: %v", err)
	}
	if err := AppendJSONL(path, map[string]string{"id": "2"}); err != nil {
		t.Fatalf("AppendJSONL second: %v", err)
	}

	assertJSONLLines(t, path, 2)
}

func TestFileStoreHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := NewFileStore(t.TempDir())
	err := store.InitRun(ctx, model.Run{ID: "run_cancelled"}, model.TaskPacket{ID: "task_cancelled"})
	if err == nil {
		t.Fatal("expected cancelled context error")
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("expected file, got directory: %s", path)
	}
}

func assertJSONLLines(t *testing.T, path string, want int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open JSONL file %s: %v", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	got := 0
	for scanner.Scan() {
		got++
		var decoded map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", got, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	if got != want {
		t.Fatalf("JSONL line count = %d, want %d", got, want)
	}
}
