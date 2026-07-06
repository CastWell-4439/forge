package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/model"
	forgexstorage "github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	forgestorage "github.com/castwell/forge/internal/storage"
)

func TestFileObserverRecordsForgeWorkflowEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	observer := NewFileObserver(FileObserverConfig{Root: root})
	workflowID := "wf-runtime-001"
	now := time.Date(2026, 7, 6, 10, 50, 0, 0, time.UTC)

	events := []*forgestorage.Event{
		{ID: 1, WorkflowID: workflowID, Type: forgestorage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{ID: 2, WorkflowID: workflowID, Type: forgestorage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now.Add(time.Second)},
		{ID: 3, WorkflowID: workflowID, TaskID: "task-1", Type: forgestorage.EventTaskScheduled, SequenceNum: 3, Timestamp: now.Add(2 * time.Second)},
		{ID: 4, WorkflowID: workflowID, TaskID: "task-1", Type: forgestorage.EventTaskStarted, SequenceNum: 4, Timestamp: now.Add(3 * time.Second)},
		{ID: 5, WorkflowID: workflowID, TaskID: "task-1", Type: forgestorage.EventTaskCompleted, Payload: json.RawMessage(`{"ok":true}`), SequenceNum: 5, Timestamp: now.Add(4 * time.Second)},
		{ID: 6, WorkflowID: workflowID, Type: forgestorage.EventWorkflowCompleted, SequenceNum: 6, Timestamp: now.Add(5 * time.Second)},
	}
	for _, event := range events {
		if err := observer.ObserveEvent(ctx, event); err != nil {
			t.Fatalf("ObserveEvent(%s): %v", event.Type, err)
		}
	}

	runID := RunIDForWorkflow(workflowID)
	layout := observer.Store().Layout()
	assertFileExists(t, layout.RunFile(runID))
	assertFileExists(t, layout.TaskPacketFile(runID))
	assertFileExists(t, layout.EventsFile(runID))
	assertFileExists(t, layout.ReportFile(runID))

	runBytes, err := os.ReadFile(layout.RunFile(runID))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var run model.Run
	if err := json.Unmarshal(runBytes, &run); err != nil {
		t.Fatalf("decode run.json: %v", err)
	}
	if run.ID != runID || run.Status != model.RunSucceeded || run.EndedAt.IsZero() {
		t.Fatalf("unexpected run metadata: %+v", run)
	}

	gotEvents := readJSONLines(t, layout.EventsFile(runID))
	if len(gotEvents) != len(events)+1 { // + report_generated
		t.Fatalf("events.jsonl lines = %d, want %d", len(gotEvents), len(events)+1)
	}
	lastForgeEvent := gotEvents[len(events)-1]
	if lastForgeEvent["type"] != string(model.EventRunFinished) {
		t.Fatalf("terminal Forge event type = %v, want %s", lastForgeEvent["type"], model.EventRunFinished)
	}
	data, ok := lastForgeEvent["data"].(map[string]any)
	if !ok || data["forge_event_type"] != string(forgestorage.EventWorkflowCompleted) {
		t.Fatalf("terminal event data mismatch: %+v", lastForgeEvent["data"])
	}

	reportBytes, err := os.ReadFile(layout.ReportFile(runID))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(reportBytes)
	if !strings.Contains(report, workflowID) || !strings.Contains(report, "observe-only") {
		t.Fatalf("report missing expected content:\n%s", report)
	}
}

func TestFileObserverRecordsFailedWorkflow(t *testing.T) {
	ctx := context.Background()
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir()})
	workflowID := "wf-runtime-failed"
	now := time.Now().UTC()

	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowStarted, SequenceNum: 1, Timestamp: now}); err != nil {
		t.Fatalf("ObserveEvent started: %v", err)
	}
	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowFailed, Payload: json.RawMessage(`{"error":"boom"}`), SequenceNum: 2, Timestamp: now.Add(time.Second)}); err != nil {
		t.Fatalf("ObserveEvent failed: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	runBytes, err := os.ReadFile(observer.Store().Layout().RunFile(runID))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var run model.Run
	if err := json.Unmarshal(runBytes, &run); err != nil {
		t.Fatalf("decode run.json: %v", err)
	}
	if run.Status != model.RunFailed {
		t.Fatalf("run status = %s, want %s", run.Status, model.RunFailed)
	}
}

func TestFileObserverRecordsTaskCallAsToolCall(t *testing.T) {
	ctx := context.Background()
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir()})
	workflowID := "wf-runtime-tool-call"
	now := time.Now().UTC()

	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowStarted, SequenceNum: 1, Timestamp: now}); err != nil {
		t.Fatalf("ObserveEvent started: %v", err)
	}
	if err := observer.ObserveTaskCall(ctx, TaskCall{
		WorkflowID: workflowID,
		TaskID:     "task-tool-1",
		TaskName:   "generate_report",
		Handler:    "ai.generate",
		WorkerID:   "worker-1",
		Input:      json.RawMessage(`{"prompt":"hello"}`),
		Output:     json.RawMessage(`{"ok":true}`),
		Success:    true,
		StartedAt:  now.Add(time.Second),
		EndedAt:    now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("ObserveTaskCall: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	lines := readJSONLines(t, observer.Store().Layout().ToolCallsFile(runID))
	if len(lines) != 1 {
		t.Fatalf("tool_calls lines = %d, want 1", len(lines))
	}
	if lines[0]["tool_name"] != "ai.generate" {
		t.Fatalf("tool call mismatch: %+v", lines[0])
	}
	args, ok := lines[0]["args"].(map[string]any)
	if !ok || args["forge_worker_id"] != "worker-1" || args["forge_task_name"] != "generate_report" {
		t.Fatalf("tool call args mismatch: %+v", lines[0]["args"])
	}
	if _, ok := lines[0]["result"].(map[string]any); !ok {
		t.Fatalf("tool call result missing: %+v", lines[0])
	}
}

func TestFileObserverRecordsFailedTaskCallAsToolCallError(t *testing.T) {
	ctx := context.Background()
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir()})
	workflowID := "wf-runtime-tool-error"
	now := time.Now().UTC()

	if err := observer.ObserveTaskCall(ctx, TaskCall{
		WorkflowID: workflowID,
		TaskID:     "task-tool-err",
		TaskName:   "run_shell",
		Handler:    "shell.exec",
		WorkerID:   "worker-2",
		Input:      json.RawMessage(`{"command":"false"}`),
		Error:      "exit status 1",
		Success:    false,
		StartedAt:  now,
		EndedAt:    now.Add(time.Second),
	}); err != nil {
		t.Fatalf("ObserveTaskCall failed call: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	lines := readJSONLines(t, observer.Store().Layout().ToolCallsFile(runID))
	if len(lines) != 1 || lines[0]["error"] != "exit status 1" {
		t.Fatalf("failed tool call mismatch: %+v", lines)
	}
}

func TestFileObserverShadowValidatesTaskCall(t *testing.T) {
	ctx := context.Background()
	contracts, err := toolgw.NewRegistry([]toolgw.ToolContract{{
		Name:            "ai.generate",
		Capability:      "generation",
		RequiredInputs:  []string{"prompt"},
		RequiredOutputs: []string{"ok"},
		Validators: []string{
			toolgw.ValidatorRequiredInputsPresent,
			toolgw.ValidatorRequiredOutputsPresent,
		},
		RiskLevel:  toolgw.RiskLow,
		SideEffect: toolgw.SideEffectReadOnly,
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir(), Authority: "L2", Contracts: contracts})
	workflowID := "wf-runtime-shadow"
	now := time.Now().UTC()

	if err := observer.ObserveTaskCall(ctx, TaskCall{
		WorkflowID: workflowID,
		TaskID:     "task-shadow-1",
		TaskName:   "generate",
		Handler:    "ai.generate",
		WorkerID:   "worker-shadow",
		Input:      json.RawMessage(`{"prompt":"hello"}`),
		Output:     json.RawMessage(`{"ok":true}`),
		Success:    true,
		StartedAt:  now,
		EndedAt:    now.Add(time.Second),
	}); err != nil {
		t.Fatalf("ObserveTaskCall: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	policyLines := readJSONLines(t, observer.Store().Layout().PolicyDecisionsFile(runID))
	if len(policyLines) != 1 || policyLines[0]["action"] != "allow" || policyLines[0]["authority"] != "L2" {
		t.Fatalf("policy decision mismatch: %+v", policyLines)
	}
	validationLines := readJSONLines(t, observer.Store().Layout().ContractValidationsFile(runID))
	if len(validationLines) != 2 {
		t.Fatalf("validation lines = %d, want 2: %+v", len(validationLines), validationLines)
	}
	for _, line := range validationLines {
		if line["status"] != "passed" {
			t.Fatalf("expected shadow validations passed, got %+v", validationLines)
		}
	}
}

func TestFileObserverShadowValidationSkipsMissingContract(t *testing.T) {
	ctx := context.Background()
	contracts, err := toolgw.NewRegistry([]toolgw.ToolContract{{Name: "known.tool", Capability: "known", RiskLevel: toolgw.RiskLow, SideEffect: toolgw.SideEffectReadOnly}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir(), Contracts: contracts})
	workflowID := "wf-runtime-shadow-missing"

	if err := observer.ObserveTaskCall(ctx, TaskCall{WorkflowID: workflowID, TaskID: "task-missing", Handler: "unknown.tool", Success: true}); err != nil {
		t.Fatalf("ObserveTaskCall: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	if _, err := os.Stat(observer.Store().Layout().PolicyDecisionsFile(runID)); !os.IsNotExist(err) {
		t.Fatalf("expected no policy_decisions for missing contract, stat err=%v", err)
	}
}

func TestFileObserverAutoIndexesTerminalRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	observer := NewFileObserver(FileObserverConfig{Root: root, AutoIndex: true})
	workflowID := "wf-runtime-index"
	now := time.Now().UTC()

	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowStarted, SequenceNum: 1, Timestamp: now}); err != nil {
		t.Fatalf("ObserveEvent started: %v", err)
	}
	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowCompleted, SequenceNum: 2, Timestamp: now.Add(time.Second)}); err != nil {
		t.Fatalf("ObserveEvent completed: %v", err)
	}

	idx, err := forgexstorage.OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex: %v", err)
	}
	defer idx.Close()
	runs, err := idx.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != RunIDForWorkflow(workflowID) || runs[0].Status != string(model.RunSucceeded) {
		t.Fatalf("indexed run mismatch: %+v", runs)
	}
}

func TestFileObserverTerminalFailureWritesBadcaseAndLessons(t *testing.T) {
	ctx := context.Background()
	observer := NewFileObserver(FileObserverConfig{
		Root: t.TempDir(),
		Taxonomy: &failure.Taxonomy{Version: 1, Rules: []failure.FailureRule{{
			ID:        "WORKER_BOOM",
			Category:  "worker_execution_failed",
			Severity:  "high",
			Retryable: false,
			Source:    "test",
			Match: failure.RuleMatch{
				MessageContains:   []string{"boom"},
				OperationContains: "worker.fail",
			},
			Recommendation: "inspect worker failure before retrying",
		}}},
	})
	workflowID := "wf-runtime-badcase"
	now := time.Now().UTC()

	if err := observer.ObserveTaskCall(ctx, TaskCall{
		WorkflowID: workflowID,
		TaskID:     "task-boom",
		TaskName:   "fail",
		Handler:    "worker.fail",
		WorkerID:   "worker-boom",
		Input:      json.RawMessage(`{"x":1}`),
		Error:      "boom happened",
		Success:    false,
		StartedAt:  now,
		EndedAt:    now.Add(time.Second),
	}); err != nil {
		t.Fatalf("ObserveTaskCall: %v", err)
	}
	if err := observer.ObserveEvent(ctx, &forgestorage.Event{WorkflowID: workflowID, Type: forgestorage.EventWorkflowFailed, SequenceNum: 2, Timestamp: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("ObserveEvent failed: %v", err)
	}

	runID := RunIDForWorkflow(workflowID)
	layout := observer.Store().Layout()
	assertFileExists(t, layout.ErrorsFile(runID))
	assertFileExists(t, layout.StopDecisionsFile(runID))
	assertFileExists(t, layout.BadCaseFile(runID))
	assertFileExists(t, layout.LessonsFile(runID))
	errors := readJSONLines(t, layout.ErrorsFile(runID))
	if len(errors) != 1 || errors[0]["category"] != "worker_execution_failed" {
		t.Fatalf("classified errors mismatch: %+v", errors)
	}
	lessons := readJSONLines(t, layout.LessonsFile(runID))
	if len(lessons) != 1 || lessons[0]["category"] != "worker_execution_failed" {
		t.Fatalf("lessons mismatch: %+v", lessons)
	}
}

func TestFileObserverRejectsInvalidEvent(t *testing.T) {
	observer := NewFileObserver(FileObserverConfig{Root: t.TempDir()})
	if err := observer.ObserveEvent(context.Background(), nil); err == nil {
		t.Fatal("expected nil event error")
	}
	if err := observer.ObserveEvent(context.Background(), &forgestorage.Event{}); err == nil {
		t.Fatal("expected missing workflow_id error")
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

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open jsonl %s: %v", path, err)
	}
	defer file.Close()

	var lines []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var decoded map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
			t.Fatalf("decode jsonl line: %v", err)
		}
		lines = append(lines, decoded)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan jsonl: %v", err)
	}
	return lines
}
