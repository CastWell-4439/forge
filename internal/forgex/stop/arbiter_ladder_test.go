package stop

import (
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

// The design rule "permission/approval required -> pause" had no signal source at
// all, so it could never fire.
func TestArbiterApprovalRequiredPauses(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceApprovalRequired, SignalSeverityHigh, model.StopActionStop, "write action needs approval", []string{"approval-1"}),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionPause {
		t.Fatalf("a pending approval must hold the run, got %+v", got)
	}
	if !strings.Contains(got.Reason, string(SignalSourceApprovalRequired)) {
		t.Fatalf("expected the reason to name the approval signal, got %q", got.Reason)
	}
}

// approval_required ranks above context_budget.
func TestArbiterApprovalBeatsContextBudget(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceContextBudget, SignalSeverityHigh, model.StopActionPause, "context budget exceeded", nil),
		NewSignal("run-1", SignalSourceApprovalRequired, SignalSeverityHigh, model.StopActionStop, "approval pending", nil),
	}
	got := NewArbiter().Decide("run-1", signals)
	if !strings.Contains(got.Reason, string(SignalSourceApprovalRequired)) {
		t.Fatalf("approval should outrank context budget, got %+v", got)
	}
}

// policy_decision still ranks above approval_required.
func TestArbiterPolicyBeatsApproval(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceApprovalRequired, SignalSeverityHigh, model.StopActionPause, "approval pending", nil),
		NewSignal("run-1", SignalSourcePolicyDecision, SignalSeverityCritical, model.StopActionStop, "policy deny external write", nil),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionStop || !strings.Contains(got.Reason, "policy") {
		t.Fatalf("policy should outrank approval, got %+v", got)
	}
}

// error_envelope is a declared input source, but it used to be missing from the
// ladder, so a blocking error signal was silently dropped.
func TestArbiterErrorEnvelopeIsRanked(t *testing.T) {
	cases := []struct {
		suggested model.StopAction
		want      model.StopAction
	}{
		{model.StopActionStop, model.StopActionStop},
		{model.StopActionPause, model.StopActionPause},
		{model.StopActionRetry, model.StopActionPause},
	}
	for _, tc := range cases {
		signals := []StopSignal{
			NewSignal("run-1", SignalSourceErrorEnvelope, SignalSeverityMedium, tc.suggested, "classified execution error", nil),
		}
		got := NewArbiter().Decide("run-1", signals)
		if got.Action != tc.want {
			t.Errorf("error_envelope suggested=%s: got %s, want %s", tc.suggested, got.Action, tc.want)
		}
		if strings.Contains(got.Reason, "no blocking termination signals") {
			t.Errorf("error_envelope suggested=%s was ignored by the arbiter", tc.suggested)
		}
	}
}

// A more specific failure signal outranks a generic error envelope.
func TestArbiterRetryBudgetBeatsErrorEnvelope(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSourceErrorEnvelope, SignalSeverityHigh, model.StopActionStop, "error", nil),
		NewSignal("run-1", SignalSourceRetryBudgetExceeded, SignalSeverityHigh, model.StopActionEscalate, "retry budget exhausted", nil),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionEscalate {
		t.Fatalf("retry budget should outrank a generic error envelope, got %+v", got)
	}
}

// An unranked source asking for a blocking action must not be silently ignored.
func TestArbiterUnrankedBlockingSignalEscalates(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSource("some_future_source"), SignalSeverityHigh, model.StopActionStop, "unknown but blocking", nil),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionEscalate {
		t.Fatalf("an unranked blocking signal must fail safe by escalating, got %+v", got)
	}
	if !strings.Contains(got.Reason, "unranked") {
		t.Fatalf("reason should say the source is unranked, got %q", got.Reason)
	}
}

// An unranked source that asks only to continue blocks nothing.
func TestArbiterUnrankedContinueSignalDoesNotEscalate(t *testing.T) {
	signals := []StopSignal{
		NewSignal("run-1", SignalSource("some_future_source"), SignalSeverityLow, model.StopActionContinue, "informational", nil),
	}
	got := NewArbiter().Decide("run-1", signals)
	if got.Action != model.StopActionContinue {
		t.Fatalf("a non-blocking unranked signal must not escalate, got %+v", got)
	}
}

// The agreed rule of engagement: the LLM ranks lowest, yet it may hold the final
// stop authority. It is therefore beaten by even the weakest deterministic
// signal, but it still decides when nothing else objects.
func TestArbiterLLMIsLowestYetDecidesWhenUncontested(t *testing.T) {
	// Beaten by the weakest ranked deterministic signal (progress_no_change).
	contested := []StopSignal{
		NewSignal("run-1", SignalSourceLLMSuggestedDone, SignalSeverityLow, model.StopActionStop, "llm says done", nil),
		NewSignal("run-1", SignalSourceProgressNoChange, SignalSeverityLow, model.StopActionPause, "no progress", nil),
	}
	got := NewArbiter().Decide("run-1", contested)
	if got.Action != model.StopActionPause {
		t.Fatalf("progress_no_change should outrank the LLM, got %+v", got)
	}

	// Uncontested: its "done" becomes the decision.
	uncontested := []StopSignal{
		NewSignal("run-1", SignalSourceLLMSuggestedDone, SignalSeverityLow, model.StopActionStop, "llm says done", nil),
	}
	got = NewArbiter().Decide("run-1", uncontested)
	if got.Action != model.StopActionStop {
		t.Fatalf("an uncontested LLM done should stop the run, got %+v", got)
	}

	// Beaten by an unranked blocking signal too: the LLM really is last.
	withUnranked := []StopSignal{
		NewSignal("run-1", SignalSourceLLMSuggestedDone, SignalSeverityLow, model.StopActionStop, "llm says done", nil),
		NewSignal("run-1", SignalSource("some_future_source"), SignalSeverityHigh, model.StopActionStop, "unknown blocker", nil),
	}
	got = NewArbiter().Decide("run-1", withUnranked)
	if got.Action != model.StopActionEscalate {
		t.Fatalf("an unranked blocker should outrank the LLM, got %+v", got)
	}
}
