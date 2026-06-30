package stop

import (
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestArbiterNoSignalsContinues(t *testing.T) {
	got := NewArbiter().Decide("run-1", nil)
	if got.Action != model.StopActionContinue {
		t.Fatalf("expected continue, got %+v", got)
	}
}

func TestArbiterHardBudgetBeatsLLMDone(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceLLMSuggestedDone, SignalSeverityLow, model.StopActionStop, "llm says done", nil),
		NewSignal("run-1", SignalSourceContextBudget, SignalSeverityHigh, model.StopActionPause, "context budget exceeded", []string{"ctx-1"}),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionPause || !strings.Contains(got.Reason, string(SignalSourceContextBudget)) {
		t.Fatalf("expected context budget pause, got %+v", got)
	}
}

func TestArbiterPolicyDenyBeatsRetry(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceErrorEnvelope, SignalSeverityMedium, model.StopActionRetry, "transient retry", nil),
		NewSignal("run-1", SignalSourcePolicyDecision, SignalSeverityHigh, model.StopActionStop, "policy deny external write", []string{"policy-1"}),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionStop || !strings.Contains(got.Reason, "policy") {
		t.Fatalf("expected policy stop, got %+v", got)
	}
}

func TestArbiterEvalFailedPauses(t *testing.T) {
	signals := []StopSignal{NewSignal("run-1", SignalSourceEvalResult, SignalSeverityHigh, model.StopActionPause, "eval failed", []string{"eval-1"})}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionPause {
		t.Fatalf("expected eval pause, got %+v", got)
	}
}

func TestArbiterContractValidationFailedStops(t *testing.T) {
	signals := []StopSignal{NewSignal("run-1", SignalSourceContractValidation, SignalSeverityHigh, model.StopActionStop, "images_refs_not_empty failed", []string{"validation-1"})}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionStop || !strings.Contains(got.Reason, "validation") {
		t.Fatalf("expected validation stop, got %+v", got)
	}
}

func TestArbiterLLMDoneOnlyWhenNoBlockingSignal(t *testing.T) {
	signals := []StopSignal{NewSignal("run-1", SignalSourceLLMSuggestedDone, SignalSeverityLow, model.StopActionStop, "llm says done", nil)}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionStop {
		t.Fatalf("expected llm done stop when no blockers, got %+v", got)
	}
}
