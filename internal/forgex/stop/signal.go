package stop

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// SignalSource describes where a termination signal came from.
type SignalSource string

const (
	SignalSourceErrorEnvelope       SignalSource = "error_envelope"
	SignalSourceContextBudget       SignalSource = "context_budget"
	SignalSourceProgressNoChange    SignalSource = "progress_no_change"
	SignalSourcePolicyDecision      SignalSource = "policy_decision"
	SignalSourceContractValidation  SignalSource = "contract_validation"
	SignalSourceEvalResult          SignalSource = "eval_result"
	SignalSourceHumanInterrupt      SignalSource = "human_interrupt"
	SignalSourceLLMSuggestedDone    SignalSource = "llm_suggested_done"
	SignalSourceRetryBudgetExceeded SignalSource = "retry_budget_exhausted"
)

// SignalSeverity describes how strongly a signal should influence arbitration.
type SignalSeverity string

const (
	SignalSeverityLow      SignalSeverity = "low"
	SignalSeverityMedium   SignalSeverity = "medium"
	SignalSeverityHigh     SignalSeverity = "high"
	SignalSeverityCritical SignalSeverity = "critical"
)

// StopSignal is one normalized signal consumed by the TerminationArbiter.
type StopSignal struct {
	ID        string           `json:"id" yaml:"id"`
	RunID     string           `json:"run_id" yaml:"run_id"`
	Source    SignalSource     `json:"source" yaml:"source"`
	Severity  SignalSeverity   `json:"severity" yaml:"severity"`
	Suggested model.StopAction `json:"suggested" yaml:"suggested"`
	Reason    string           `json:"reason" yaml:"reason"`
	Evidence  []string         `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	CreatedAt time.Time        `json:"created_at" yaml:"created_at"`
}

var signalSeq atomic.Uint64

// NewSignal creates a normalized stop signal with a generated id.
func NewSignal(runID string, source SignalSource, severity SignalSeverity, suggested model.StopAction, reason string, evidence []string) StopSignal {
	seq := signalSeq.Add(1)
	if strings.TrimSpace(runID) == "" {
		runID = "run"
	}
	return StopSignal{
		ID:        fmt.Sprintf("signal-%s-%d", runID, seq),
		RunID:     runID,
		Source:    source,
		Severity:  severity,
		Suggested: suggested,
		Reason:    strings.TrimSpace(reason),
		Evidence:  cleanEvidence(evidence),
		CreatedAt: time.Now().UTC(),
	}
}

// EvidenceSummary renders evidence ids as a compact comma-separated string.
func EvidenceSummary(signals []StopSignal) []string {
	out := make([]string, 0, len(signals))
	for _, signal := range signals {
		if strings.TrimSpace(signal.ID) != "" {
			out = append(out, signal.ID)
		}
		out = append(out, cleanEvidence(signal.Evidence)...)
	}
	return out
}

func cleanEvidence(evidence []string) []string {
	out := make([]string, 0, len(evidence))
	for _, item := range evidence {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
