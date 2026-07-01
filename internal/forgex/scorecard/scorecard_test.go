package scorecard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
)

func TestBuildSuccessScorecard(t *testing.T) {
	artifacts := forgexeval.RunArtifacts{
		Run:                 model.Run{ID: "run_success", Status: model.RunSucceeded},
		PolicyDecisions:     []model.PolicyDecision{{Action: "allow"}},
		ContractValidations: []model.ContractValidation{{Status: "passed"}},
		Artifacts:           []model.ArtifactRecord{{Status: model.ArtifactProduced}},
	}
	evalResult := model.EvalResult{
		RunID:   "run_success",
		SuiteID: "generic_contract_happy_v1",
		Status:  model.EvalPassed,
		Cases:   []model.EvalCaseResult{{ID: "GENERIC_REQUIRED_ASSETS_PRESENT", Status: model.EvalPassed}},
	}

	card := Build(artifacts, evalResult, 0)
	if card.Overall != VerdictPass {
		t.Fatalf("Overall = %q, want pass", card.Overall)
	}
	if card.Dimensions["task_success"] != VerdictPass {
		t.Fatalf("task_success = %q, want pass", card.Dimensions["task_success"])
	}
	if card.Dimensions["tool_usage_quality"] != VerdictPass {
		t.Fatalf("tool_usage_quality = %q, want pass", card.Dimensions["tool_usage_quality"])
	}
	if card.Dimensions["operational_quality"] != VerdictUnknown {
		t.Fatalf("operational_quality = %q, want unknown", card.Dimensions["operational_quality"])
	}
}

func TestBuildContractViolationScorecard(t *testing.T) {
	artifacts := forgexeval.RunArtifacts{
		Run:                 model.Run{ID: "run_violation", Status: model.RunStopped},
		PolicyDecisions:     []model.PolicyDecision{{Action: "allow"}},
		ContractValidations: []model.ContractValidation{{Status: "passed"}, {Status: "failed"}},
		Artifacts:           []model.ArtifactRecord{{Status: model.ArtifactMissing}},
		Errors:              []model.ErrorEnvelope{{Category: "tool_contract_violation"}},
		StopDecisions:       []model.StopDecision{{Action: model.StopActionStop}},
	}
	evalResult := model.EvalResult{
		RunID:   "run_violation",
		SuiteID: "generic_contract_regression_v1",
		Status:  model.EvalPassed,
		Cases:   []model.EvalCaseResult{{ID: "GENERIC_REQUIRED_ASSETS_EMPTY", Status: model.EvalPassed}},
	}

	card := Build(artifacts, evalResult, 1)
	if card.Overall != VerdictFail {
		t.Fatalf("Overall = %q, want fail", card.Overall)
	}
	if card.Dimensions["task_success"] != VerdictPass {
		t.Fatalf("task_success = %q, want pass for expected stop", card.Dimensions["task_success"])
	}
	if card.Dimensions["tool_usage_quality"] != VerdictFail {
		t.Fatalf("tool_usage_quality = %q, want fail", card.Dimensions["tool_usage_quality"])
	}
	if card.Metrics.ValidationFailed != 1 || card.Metrics.ArtifactsMissing != 1 || card.Metrics.Lessons != 1 {
		t.Fatalf("Metrics = %+v, want validation/artifacts/lessons counts", card.Metrics)
	}
}

func TestWriteScorecard(t *testing.T) {
	dir := t.TempDir()
	card := Scorecard{RunID: "run_1", SuiteID: "suite", Overall: VerdictPass, Dimensions: map[string]string{"task_success": VerdictPass}}
	if err := Write(dir, card); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "scorecard.json"))
	if err != nil {
		t.Fatalf("read scorecard: %v", err)
	}
	var decoded Scorecard
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode scorecard: %v", err)
	}
	if decoded.RunID != "run_1" || decoded.Overall != VerdictPass {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestAppendToReportMissingReportIsNoop(t *testing.T) {
	dir := t.TempDir()
	card := Scorecard{RunID: "run_1", SuiteID: "suite", Overall: VerdictPass}
	if err := AppendToReport(dir, card); err != nil {
		t.Fatalf("AppendToReport() error = %v", err)
	}
}

func TestAppendToReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte("# Report\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	card := Scorecard{
		RunID:   "run_1",
		SuiteID: "suite",
		Overall: VerdictPass,
		Dimensions: map[string]string{
			"task_success":        VerdictPass,
			"tool_usage_quality":  VerdictPass,
			"control_quality":     VerdictPass,
			"operational_quality": VerdictUnknown,
		},
	}
	if err := AppendToReport(dir, card); err != nil {
		t.Fatalf("AppendToReport() error = %v", err)
	}
	if err := AppendToReport(dir, card); err != nil {
		t.Fatalf("AppendToReport() second call error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)
	if got := strings.Count(content, "## Scorecard"); got != 1 {
		t.Fatalf("Scorecard section count = %d, want 1\n%s", got, content)
	}
	if !strings.Contains(content, "- **Overall**: pass") {
		t.Fatalf("report missing overall verdict:\n%s", content)
	}
}
