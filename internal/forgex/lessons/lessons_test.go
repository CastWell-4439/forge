package lessons_test

import (
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/lessons"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/report"
)

// violationSnapshot builds a snapshot resembling the empty required_assets bad
// case: a classified contract violation that drove a stop decision.
func violationSnapshot() report.RunSnapshot {
	ts := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	return report.RunSnapshot{
		Run: model.Run{
			ID:        "run_violation_001",
			Status:    model.RunStopped,
			StartedAt: ts,
			EndedAt:   ts.Add(2 * time.Second),
		},
		ContractValidations: []model.ContractValidation{
			{ID: "cv_1", ToolName: "demo.expensive_generation", Status: "failed", Message: "required_assets must be non-empty"},
			{ID: "cv_ok", ToolName: "demo.expensive_generation", Status: "passed"},
		},
		Errors: []model.ErrorEnvelope{
			{
				ID:          "err_1",
				Source:      "tool_contract",
				Operation:   "demo.expensive_generation",
				Message:     "required_assets is empty",
				Category:    "tool_contract_violation",
				Severity:    "high",
				Fingerprint: "abcd1234abcd1234",
				Metadata: map[string]string{
					"rule_id":        "GENERIC_REQUIRED_ASSETS_EMPTY",
					"recommendation": "validate required assets before execution",
				},
			},
		},
		StopDecisions: []model.StopDecision{
			{ID: "dec_1", Action: model.StopActionStop, Reason: "contract violation is not retryable"},
		},
	}
}

func TestDeriveViolationYieldsLesson(t *testing.T) {
	got := lessons.Derive(violationSnapshot())
	if len(got) != 1 {
		t.Fatalf("Derive() returned %d lessons, want 1", len(got))
	}
	l := got[0]

	if l.ID != "LESSON_GENERIC_REQUIRED_ASSETS_EMPTY" {
		t.Errorf("ID = %q, want LESSON_GENERIC_REQUIRED_ASSETS_EMPTY", l.ID)
	}
	if l.SourceRunID != "run_violation_001" {
		t.Errorf("SourceRunID = %q, want run_violation_001", l.SourceRunID)
	}
	if l.Category != "tool_contract_violation" {
		t.Errorf("Category = %q, want tool_contract_violation", l.Category)
	}
	if l.Content != "validate required assets before execution" {
		t.Errorf("Content = %q, want the recommendation text", l.Content)
	}
	if l.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the run end time")
	}
	if want := "required_assets is empty for demo.expensive_generation"; l.Metadata["trigger"] != want {
		t.Errorf("trigger = %q, want %q", l.Metadata["trigger"], want)
	}
	if l.Metadata["rule"] != "GENERIC_REQUIRED_ASSETS_EMPTY" {
		t.Errorf("rule = %q, want GENERIC_REQUIRED_ASSETS_EMPTY", l.Metadata["rule"])
	}
	if l.Metadata["final_decision"] != string(model.StopActionStop) {
		t.Errorf("final_decision = %q, want %q", l.Metadata["final_decision"], model.StopActionStop)
	}
	// Evidence must reference the error and only the failed contract validation.
	if want := "err_1,cv_1"; l.Metadata["evidence"] != want {
		t.Errorf("evidence = %q, want %q", l.Metadata["evidence"], want)
	}
}

func TestDeriveCleanRunYieldsNoLessons(t *testing.T) {
	snapshot := report.RunSnapshot{
		Run: model.Run{ID: "run_ok", Status: model.RunSucceeded},
		StopDecisions: []model.StopDecision{
			{ID: "dec_1", Action: model.StopActionContinue, Reason: "contract satisfied"},
		},
	}
	if got := lessons.Derive(snapshot); got != nil {
		t.Fatalf("Derive() on a clean run = %v, want nil", got)
	}
}

func TestDeriveHaltingWithoutErrorsYieldsNoLessons(t *testing.T) {
	snapshot := report.RunSnapshot{
		Run: model.Run{ID: "run_stop", Status: model.RunStopped},
		StopDecisions: []model.StopDecision{
			{ID: "dec_1", Action: model.StopActionStop, Reason: "stopped for another reason"},
		},
	}
	if got := lessons.Derive(snapshot); got != nil {
		t.Fatalf("Derive() with no errors = %v, want nil", got)
	}
}
