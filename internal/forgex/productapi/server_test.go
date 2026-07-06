package productapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/reliability"
	"github.com/castwell/forge/internal/forgex/scorecard"
	"github.com/castwell/forge/internal/forgex/storage"
)

func TestProductAPIServesRunArtifacts(t *testing.T) {
	root := t.TempDir()
	runID := "run_api_test"
	now := time.Date(2026, 7, 6, 5, 0, 0, 0, time.UTC)
	store := storage.NewFileStore(root)
	run := model.Run{ID: runID, TaskID: "task_api", Name: "api demo", Status: model.RunSucceeded, StartedAt: now, EndedAt: now.Add(time.Minute)}
	packet := model.TaskPacket{ID: "task_api", Name: "api demo", Goal: "serve local product API", Metadata: map[string]string{"project": "forgex"}}
	if err := store.InitRun(context.Background(), run, packet); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}
	if err := store.AppendEvent(context.Background(), model.Event{ID: "evt_1", RunID: runID, Type: model.EventRunStarted, Message: "started", Timestamp: now}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.AppendToolCall(context.Background(), model.ToolCall{ID: "call_1", RunID: runID, ToolName: "demo.tool", StartedAt: now, EndedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("AppendToolCall() error = %v", err)
	}
	if err := store.AppendPolicyDecision(context.Background(), model.PolicyDecision{ID: "pol_1", RunID: runID, ToolName: "demo.tool", Action: "allow", CreatedAt: now}); err != nil {
		t.Fatalf("AppendPolicyDecision() error = %v", err)
	}
	if err := store.AppendContractValidation(context.Background(), model.ContractValidation{ID: "val_1", RunID: runID, ToolName: "demo.tool", Status: "passed", CreatedAt: now}); err != nil {
		t.Fatalf("AppendContractValidation() error = %v", err)
	}
	if err := store.AppendError(context.Background(), model.ErrorEnvelope{ID: "err_1", RunID: runID, Source: "tool", Operation: "call", Message: "boom", Timestamp: now}); err != nil {
		t.Fatalf("AppendError() error = %v", err)
	}
	if err := store.AppendLesson(context.Background(), model.Lesson{ID: "lesson_1", SourceRunID: runID, Title: "remember", Category: "test", Content: "keep product API stable", CreatedAt: now}); err != nil {
		t.Fatalf("AppendLesson() error = %v", err)
	}
	if err := store.AppendArtifact(context.Background(), model.ArtifactRecord{ID: "asset_1", RunID: runID, Type: "report", Status: model.ArtifactProduced, URI: "report.md", Metadata: map[string]string{"content_type": "text/markdown"}, CreatedAt: now}); err != nil {
		t.Fatalf("AppendArtifact() error = %v", err)
	}
	if err := store.AppendStopDecision(context.Background(), model.StopDecision{ID: "stop_1", RunID: runID, Action: model.StopActionContinue, Reason: "healthy", DecidedAt: now}); err != nil {
		t.Fatalf("AppendStopDecision() error = %v", err)
	}
	if err := store.AppendContextPack(context.Background(), model.ContextPack{ID: "ctx_1", RunID: runID, Purpose: "api test", Summary: "context pack", CreatedAt: now}); err != nil {
		t.Fatalf("AppendContextPack() error = %v", err)
	}
	if err := store.AppendGateDecision(context.Background(), model.GateDecision{ID: "gate_1", RunID: runID, Mode: model.GateModeShadow, Action: model.GateActionPause, Reason: "needs review", NeedsHuman: true, CreatedAt: now}); err != nil {
		t.Fatalf("AppendGateDecision() error = %v", err)
	}
	if err := store.AppendHITLReview(context.Background(), model.HITLReview{ID: "hitl_1", RunID: runID, GateID: "gate_1", Status: model.HITLReviewPending, Reason: "waiting", CreatedAt: now}); err != nil {
		t.Fatalf("AppendHITLReview() error = %v", err)
	}
	if err := store.SaveWorldState(context.Background(), model.WorldState{RunID: runID, Version: 1, Entries: []model.StateEntry{{Key: "phase", Status: model.StateAccepted, Producer: "test", Version: 1}}, UpdatedAt: now}); err != nil {
		t.Fatalf("SaveWorldState() error = %v", err)
	}
	if err := store.AppendStateClaim(context.Background(), model.StateClaim{ID: "claim_1", RunID: runID, Key: "phase", Producer: "test", Status: model.StateAccepted, CreatedAt: now}); err != nil {
		t.Fatalf("AppendStateClaim() error = %v", err)
	}
	evalResult := model.EvalResult{ID: "eval_1", RunID: runID, SuiteID: "suite_1", Status: model.EvalPassed, CreatedAt: now}
	if err := writeTestJSON(filepath.Join(root, "runs", runID, "eval_result.json"), evalResult); err != nil {
		t.Fatalf("write eval result: %v", err)
	}
	card := scorecard.Scorecard{RunID: runID, SuiteID: "suite_1", Overall: scorecard.VerdictPass, Dimensions: map[string]string{"task_success": scorecard.VerdictPass}, CreatedAt: now}
	if err := writeTestJSON(filepath.Join(root, "runs", runID, "scorecard.json"), card); err != nil {
		t.Fatalf("write scorecard: %v", err)
	}
	if err := store.WriteReport(context.Background(), runID, "# Report\n\nOK\n"); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}
	badcase := []byte("id: FORGEX_BADCASE_TOOL_CONTRACT\ntitle: Tool contract badcase\nrun_id: " + runID + "\nfailure_category: tool_contract\nexpected_decision: pause\nassertions:\n  - expected pause\n")
	if err := store.WriteBadCase(context.Background(), runID, badcase); err != nil {
		t.Fatalf("WriteBadCase() error = %v", err)
	}
	repeat := reliability.RepeatResult{CaseID: "case_1", SuiteID: "suite_1", Total: 1, Passed: 1, PassAtK: true, PassAll: true, CreatedAt: now}
	if err := writeTestJSON(filepath.Join(root, "repeat_result.json"), repeat); err != nil {
		t.Fatalf("write repeat result: %v", err)
	}

	h := New(Config{Root: root, Version: "test"}).Handler()

	assertStatus(t, h, "/healthz", http.StatusOK)
	assertStatus(t, h, "/api/v1/version", http.StatusOK)

	body := getJSON(t, h, "/api/v1/runs", http.StatusOK)
	var runs struct {
		Runs []RunSummary `json:"runs"`
	}
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].ID != runID || runs.Runs[0].AssetCount != 1 || runs.Runs[0].ErrorCount != 1 {
		t.Fatalf("unexpected runs response: %+v", runs.Runs)
	}

	body = getJSON(t, h, "/api/v1/runs/"+runID, http.StatusOK)
	var detail RunDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Run.ID != runID || detail.Project != "forgex" || detail.Metrics.ToolCalls != 1 || detail.Metrics.Lessons != 1 {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	assertContains(t, h, "/api/v1/runs/"+runID+"/events", "run_started")
	assertContains(t, h, "/api/v1/runs/"+runID+"/timeline", "run_started")
	assertContains(t, h, "/api/v1/runs/"+runID+"/tool-calls", "demo.tool")
	assertContains(t, h, "/api/v1/runs/"+runID+"/policy-decisions", "allow")
	assertContains(t, h, "/api/v1/runs/"+runID+"/contract-validations", "passed")
	assertContains(t, h, "/api/v1/runs/"+runID+"/errors", "boom")
	assertContains(t, h, "/api/v1/runs/"+runID+"/lessons", "keep product API stable")
	assertContains(t, h, "/api/v1/runs/"+runID+"/report", "# Report")
	assertContains(t, h, "/api/v1/runs/"+runID+"/badcase", "Tool contract badcase")
	postJSON(t, h, "/api/v1/runs/"+runID+"/promotion-draft", `{}`, http.StatusCreated)
	assertContains(t, h, "/api/v1/runs/"+runID+"/promotion-draft", "review_required")
	assertContains(t, h, "/api/v1/reliability/repeat-result", "case_1")
	assertContains(t, h, "/api/v1/runs/"+runID+"/explorer", "scorecard")
	assertContains(t, h, "/api/v1/runs/"+runID+"/artifacts", "asset_1")
	assertContains(t, h, "/api/v1/runs/"+runID+"/stop-decisions", "healthy")
	assertContains(t, h, "/api/v1/runs/"+runID+"/gate-decisions", "needs review")
	assertContains(t, h, "/api/v1/runs/"+runID+"/hitl-reviews", "waiting")
	assertContains(t, h, "/api/v1/runs/"+runID+"/context-packs", "context pack")
	assertContains(t, h, "/api/v1/runs/"+runID+"/state", "phase")
	assertContains(t, h, "/api/v1/runs/"+runID+"/state-claims", "claim_1")
	assertContains(t, h, "/api/v1/runs/"+runID+"/eval-result", "suite_1")
	assertContains(t, h, "/api/v1/runs/"+runID+"/scorecard", "pass")
	assertContains(t, h, "/api/v1/overview", "succeeded_runs")
	assertContains(t, h, "/api/v1/overview", "projects")
	assertContains(t, h, "/api/v1/workspaces", "Local Workspace")
	assertContains(t, h, "/api/v1/projects", "forgex")
	assertContains(t, h, "/api/v1/projects/forgex", "forgex")
	assertContains(t, h, "/api/v1/projects/forgex/runs", runID)
	assertContains(t, h, "/api/v1/runs?project=forgex", runID)
	postJSON(t, h, "/api/v1/runs/"+runID+"/gate-decisions", `{"action":"escalate","reason":"manual shadow gate"}`, http.StatusCreated)
	postJSON(t, h, "/api/v1/runs/"+runID+"/hitl-reviews", `{"gate_id":"gate_manual","status":"approved","reviewer":"tester","reason":"safe to continue"}`, http.StatusCreated)
	assertContains(t, h, "/api/v1/runs/"+runID+"/gate-decisions", "manual shadow gate")
	assertContains(t, h, "/api/v1/runs/"+runID+"/hitl-reviews", "safe to continue")
	assertContains(t, h, "/api/v1/assets", "asset_1")
	assertContains(t, h, "/api/v1/assets", "by_kind")
}

func TestProductAPINotFoundAndInvalidRunID(t *testing.T) {
	h := New(Config{Root: t.TempDir(), Version: "test"}).Handler()
	assertStatus(t, h, "/api/v1/runs/missing", http.StatusNotFound)
	assertStatus(t, h, "/api/v1/runs/bad%5Cid/events", http.StatusBadRequest)
	assertStatus(t, h, "/api/v1/runs/missing/unknown", http.StatusNotFound)
	assertStatus(t, h, "/api/v1/projects/missing", http.StatusNotFound)
	assertStatus(t, h, "/api/v1/projects/bad%5Cid", http.StatusBadRequest)
}

func writeTestJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func assertStatus(t *testing.T, h http.Handler, path string, want int) {
	t.Helper()
	_ = getJSON(t, h, path, want)
}

func assertContains(t *testing.T, h http.Handler, path string, want string) {
	t.Helper()
	body := string(getJSON(t, h, path, http.StatusOK))
	if !strings.Contains(body, want) {
		t.Fatalf("response for %s does not contain %q: %s", path, want, body)
	}
}

func postJSON(t *testing.T, h http.Handler, path string, body string, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("POST %s status = %d, want %d, body=%s", path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func getJSON(t *testing.T, h http.Handler, path string, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("GET %s status = %d, want %d, body=%s", path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.Bytes()
}
