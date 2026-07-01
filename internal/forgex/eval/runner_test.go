package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

func TestResolvePath(t *testing.T) {
	artifacts := RunArtifacts{
		Errors:        []model.ErrorEnvelope{{Category: "tool_contract_violation"}},
		StopDecisions: []model.StopDecision{{Action: model.StopActionStop}},
	}

	got, err := ResolvePath(artifacts, "errors[0].category")
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if got != "tool_contract_violation" {
		t.Fatalf("ResolvePath() = %q", got)
	}

	got, err = ResolvePath(artifacts, "stop_decisions[0].action")
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if got != "stop" {
		t.Fatalf("ResolvePath() = %q", got)
	}
}

func TestRunWritesEvalResult(t *testing.T) {
	root := t.TempDir()
	runID := "run_eval_test"
	store := storage.NewFileStore(root)
	packet := model.TaskPacket{ID: "task_eval", Name: "eval demo", Goal: "verify run"}
	run := model.Run{ID: runID, TaskID: packet.ID, Name: packet.Name, Status: model.RunStopped, StartedAt: time.Now().UTC()}
	if err := store.InitRun(context.Background(), run, packet); err != nil {
		t.Fatalf("InitRun() error = %v", err)
	}
	if err := store.AppendError(context.Background(), model.ErrorEnvelope{ID: "err_1", RunID: runID, Category: "tool_contract_violation", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatalf("AppendError() error = %v", err)
	}
	if err := store.AppendStopDecision(context.Background(), model.StopDecision{ID: "decision_1", RunID: runID, Action: model.StopActionStop, DecidedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AppendStopDecision() error = %v", err)
	}

	rulesPath := filepath.Join(t.TempDir(), "eval_rules.yaml")
	if err := os.WriteFile(rulesPath, []byte(`version: 1
suites:
  - id: generic_contract_regression_v1
    cases:
      - id: GENERIC_REQUIRED_ASSETS_EMPTY
        assertions:
          - path: errors[0].category
            op: eq
            value: tool_contract_violation
          - path: stop_decisions[0].action
            op: eq
            value: stop
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := Run(context.Background(), filepath.Join(root, "runs", runID), rulesPath, "generic_contract_regression_v1")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.EvalPassed {
		t.Fatalf("Run() status = %s", result.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", runID, "eval_result.json")); err != nil {
		t.Fatalf("eval_result.json missing: %v", err)
	}
}

func TestRunFailsOnUnexpectedValue(t *testing.T) {
	artifacts := RunArtifacts{Errors: []model.ErrorEnvelope{{Category: "unknown"}}}
	caseResult := evaluateCase(artifacts, Case{
		ID:         "case_1",
		Assertions: []Assertion{{Path: "errors[0].category", Op: "eq", Value: "tool_contract_violation"}},
	})
	if caseResult.Status != model.EvalFailed {
		t.Fatalf("case status = %s", caseResult.Status)
	}
	if caseResult.Assertions[0].Status != model.EvalFailed {
		t.Fatalf("assertion status = %s", caseResult.Assertions[0].Status)
	}
}
