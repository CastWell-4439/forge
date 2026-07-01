package metrics

import (
	"fmt"

	"github.com/castwell/forge/internal/forgex/model"
)

// Control-plane action markers used when deriving metrics from persisted
// streams. They mirror the string values written by the policy engine and stop
// signal sources; metrics compares against these literals to avoid importing the
// engine packages.
const (
	policyActionDeny            = "deny"
	policyActionRequireApproval = "require_approval"
	contractStatusFailed        = "failed"
	stopSourceProgressNoChange  = "progress_no_change"
)

// ControlMetrics summarizes the control-plane outcomes of a single run.
//
// Every field is a non-negative count. A zero value is meaningful: it means the
// corresponding stream was absent or contained no matching records, which is a
// valid state for older runs indexed before a stream existed.
type ControlMetrics struct {
	PolicyDecisionCount           int `json:"policy_decision_count" yaml:"policy_decision_count"`
	PolicyDenyCount               int `json:"policy_deny_count" yaml:"policy_deny_count"`
	ApprovalRequiredCount         int `json:"approval_required_count" yaml:"approval_required_count"`
	ContractValidationFailedCount int `json:"contract_validation_failed_count" yaml:"contract_validation_failed_count"`
	SafeStopCount                 int `json:"safe_stop_count" yaml:"safe_stop_count"`
	PauseCount                    int `json:"pause_count" yaml:"pause_count"`
	ContextBudgetExceededCount    int `json:"context_budget_exceeded_count" yaml:"context_budget_exceeded_count"`
	ProgressNoChangeCount         int `json:"progress_no_change_count" yaml:"progress_no_change_count"`
	MissingArtifactCount          int `json:"missing_artifact_count" yaml:"missing_artifact_count"`
	StateConflictCount            int `json:"state_conflict_count" yaml:"state_conflict_count"`
}

// Inputs bundles the run streams needed to compute ControlMetrics. Any field may
// be nil; a nil slice or WorldState contributes zero to the derived counts.
type Inputs struct {
	PolicyDecisions     []model.PolicyDecision
	ContractValidations []model.ContractValidation
	StopDecisions       []model.StopDecision
	StopSignals         []model.StopSignalRecord
	ContextPacks        []model.ContextPack
	Artifacts           []model.ArtifactRecord
	StateClaims         []model.StateClaim
	WorldState          *model.WorldState
}

// Compute derives ControlMetrics from the supplied run streams.
//
// The mapping from stream to metric is intentionally direct so the numbers are
// auditable against the raw artifacts:
//   - policy decisions: total, deny action, and HITL/approval requirement;
//   - contract validations: failed status;
//   - stop decisions: safe stop and pause actions;
//   - context packs: budget exceeded;
//   - stop signals: progress-no-change source;
//   - artifacts: missing status;
//   - world state + state claims: conflicted status.
func Compute(in Inputs) ControlMetrics {
	var m ControlMetrics

	m.PolicyDecisionCount = len(in.PolicyDecisions)
	for _, d := range in.PolicyDecisions {
		if d.Action == policyActionDeny {
			m.PolicyDenyCount++
		}
		if d.RequiresHITL || d.Action == policyActionRequireApproval {
			m.ApprovalRequiredCount++
		}
	}

	for _, v := range in.ContractValidations {
		if v.Status == contractStatusFailed {
			m.ContractValidationFailedCount++
		}
	}

	for _, d := range in.StopDecisions {
		switch d.Action {
		case model.StopActionStop:
			m.SafeStopCount++
		case model.StopActionPause:
			m.PauseCount++
		}
	}

	for _, p := range in.ContextPacks {
		if p.BudgetExceeded {
			m.ContextBudgetExceededCount++
		}
	}

	for _, s := range in.StopSignals {
		if s.Source == stopSourceProgressNoChange {
			m.ProgressNoChangeCount++
		}
	}

	for _, a := range in.Artifacts {
		if a.Status == model.ArtifactMissing {
			m.MissingArtifactCount++
		}
	}

	if in.WorldState != nil {
		for _, e := range in.WorldState.Entries {
			if e.Status == model.StateConflicted {
				m.StateConflictCount++
			}
		}
	}
	for _, c := range in.StateClaims {
		if c.Status == model.StateConflicted {
			m.StateConflictCount++
		}
	}

	return m
}

// Summary renders a single-line control-plane summary for CLI listings, e.g.
// "policy=2 deny=1 approval=0 validation_failed=1 artifacts_missing=1 conflicts=0".
func (m ControlMetrics) Summary() string {
	return fmt.Sprintf("policy=%d deny=%d approval=%d validation_failed=%d artifacts_missing=%d conflicts=%d",
		m.PolicyDecisionCount, m.PolicyDenyCount, m.ApprovalRequiredCount,
		m.ContractValidationFailedCount, m.MissingArtifactCount, m.StateConflictCount)
}
