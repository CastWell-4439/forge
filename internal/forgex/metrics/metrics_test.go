package metrics

import (
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestComputeCountsAllStreams(t *testing.T) {
	in := Inputs{
		PolicyDecisions: []model.PolicyDecision{
			{Action: "allow"},
			{Action: "deny"},
			{Action: "require_approval"},
			{Action: "allow", RequiresHITL: true},
		},
		ContractValidations: []model.ContractValidation{
			{Status: "passed"},
			{Status: "failed"},
			{Status: "failed"},
		},
		StopDecisions: []model.StopDecision{
			{Action: model.StopActionStop},
			{Action: model.StopActionPause},
			{Action: model.StopActionContinue},
		},
		StopSignals: []model.StopSignalRecord{
			{Source: "progress_no_change"},
			{Source: "contract_validation"},
			{Source: "progress_no_change"},
		},
		ContextPacks: []model.ContextPack{
			{BudgetExceeded: true},
			{BudgetExceeded: false},
		},
		Artifacts: []model.ArtifactRecord{
			{Status: model.ArtifactMissing},
			{Status: model.ArtifactValid},
			{Status: model.ArtifactMissing},
		},
		StateClaims: []model.StateClaim{
			{Status: model.StateConflicted},
			{Status: model.StateAccepted},
		},
		WorldState: &model.WorldState{
			Entries: []model.StateEntry{
				{Status: model.StateConflicted},
				{Status: model.StateAccepted},
			},
		},
	}

	got := Compute(in)
	want := ControlMetrics{
		PolicyDecisionCount:           4,
		PolicyDenyCount:               1,
		ApprovalRequiredCount:         2, // require_approval action + RequiresHITL flag
		ContractValidationFailedCount: 2,
		SafeStopCount:                 1,
		PauseCount:                    1,
		ContextBudgetExceededCount:    1,
		ProgressNoChangeCount:         2,
		MissingArtifactCount:          2,
		StateConflictCount:            2, // one world state entry + one claim
	}
	if got != want {
		t.Fatalf("Compute() = %+v, want %+v", got, want)
	}
}

func TestComputeEmptyInputsIsZero(t *testing.T) {
	got := Compute(Inputs{})
	if (got != ControlMetrics{}) {
		t.Fatalf("Compute(empty) = %+v, want zero value", got)
	}
}

func TestSummaryFormat(t *testing.T) {
	m := ControlMetrics{
		PolicyDecisionCount:           2,
		PolicyDenyCount:               1,
		ApprovalRequiredCount:         0,
		ContractValidationFailedCount: 1,
		MissingArtifactCount:          1,
		StateConflictCount:            0,
	}
	want := "policy=2 deny=1 approval=0 validation_failed=1 artifacts_missing=1 conflicts=0"
	if got := m.Summary(); got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}
