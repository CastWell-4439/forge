package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// errorGate always fails, simulating a policy file that cannot be read or a
// review resolver that times out.
type errorGate struct {
	calls int
}

func (g *errorGate) BeforeExecute(ctx context.Context, req GateRequest) (GateDecision, error) {
	g.calls++
	return GateDecision{}, errors.New("policy store unavailable")
}

func newDemoExecutor(called *bool) *Executor {
	registry := NewRegistry()
	registry.Register("demo", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		if called != nil {
			*called = true
		}
		return map[string]interface{}{"ok": true}, nil
	})
	return NewExecutor(registry)
}

func demoRequest() *forgev1.TaskRequest {
	return &forgev1.TaskRequest{TaskId: "task_1", WorkflowId: "wf_1", TaskName: "Demo", Handler: "demo"}
}

func TestNormalizeGateAction(t *testing.T) {
	cases := []struct {
		in   GateAction
		want GateAction
	}{
		{GateActionAllow, GateActionAllow},
		{GateActionBlock, GateActionBlock},
		{GateActionPause, GateActionPause},
		{GateActionRetry, GateActionRetry},
		{GateActionEscalate, GateActionEscalate},
		// Malformed or missing actions must never be silently allowed.
		{"", GateActionBlock},
		{"bogus", GateActionBlock},
	}
	for _, tc := range cases {
		if got := NormalizeGateAction(tc.in); got != tc.want {
			t.Errorf("NormalizeGateAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGateActionExecutesHandler(t *testing.T) {
	if !GateActionExecutesHandler(GateActionAllow) {
		t.Fatalf("allow must let the handler run")
	}
	for _, action := range []GateAction{GateActionBlock, GateActionPause, GateActionRetry, GateActionEscalate, "", "bogus"} {
		if GateActionExecutesHandler(action) {
			t.Errorf("action %q must not let the handler run", action)
		}
	}
}

// TestExecutorGateActionSemantics pins the runtime effect of every action, so the
// documented contract on RuntimeGate is enforced rather than merely described.
//
// There are three outcomes, not two: allow runs the handler, block/escalate/
// retry refuse it as a failure, and pause parks it awaiting a human. A paused
// task must be distinguishable at the coordinator from a failed one — the whole
// reason PAUSED exists.
func TestExecutorGateActionSemantics(t *testing.T) {
	cases := []struct {
		action        GateAction
		enforce       bool
		wantHandler   bool
		wantSuccess   bool
		wantPaused    bool
		wantActionTag string
	}{
		{GateActionAllow, true, true, true, false, ""},
		{GateActionAllow, false, true, true, false, ""},
		// Shadow: the action is recorded but execution is unchanged.
		{GateActionBlock, false, true, true, false, ""},
		{GateActionPause, false, true, true, false, ""},
		// Enforce: block/escalate/retry stop the handler and report a failure.
		{GateActionBlock, true, false, false, false, "block"},
		{GateActionRetry, true, false, false, false, "retry"},
		{GateActionEscalate, true, false, false, false, "escalate"},
		// Enforce: pause stops the handler too, but reports a hold rather than a
		// failure, so the coordinator can park and later resume the task.
		{GateActionPause, true, false, false, true, ""},
		// A malformed action fails safe.
		{"", true, false, false, false, "block"},
		{"bogus", true, false, false, false, "block"},
	}

	for _, tc := range cases {
		called := false
		gate := &testGate{decision: GateDecision{ID: "gate_1", Action: tc.action, Reason: "because", Enforce: tc.enforce}}
		executor := newDemoExecutor(&called).WithRuntimeGate(gate)

		resp := executor.Execute(context.Background(), demoRequest())

		if called != tc.wantHandler {
			t.Errorf("action=%q enforce=%v: handler called=%v, want %v", tc.action, tc.enforce, called, tc.wantHandler)
		}
		if resp.GetSuccess() != tc.wantSuccess {
			t.Errorf("action=%q enforce=%v: success=%v, want %v (err=%q)", tc.action, tc.enforce, resp.GetSuccess(), tc.wantSuccess, resp.GetErrorMsg())
		}

		// A hold is its own outcome: paused set, no failure text, reason carried.
		if tc.wantPaused {
			if !resp.GetPaused() {
				t.Errorf("action=%q enforce=%v: paused=false, want a hold reported as paused", tc.action, tc.enforce)
			}
			if resp.GetErrorMsg() != "" {
				t.Errorf("action=%q enforce=%v: a hold must not report a failure, got %q", tc.action, tc.enforce, resp.GetErrorMsg())
			}
			if resp.GetPauseReason() == "" {
				t.Errorf("action=%q enforce=%v: a hold must carry its reason for the reviewer", tc.action, tc.enforce)
			}
			if resp.GetGateId() != "gate_1" {
				t.Errorf("action=%q enforce=%v: gate id = %q, want gate_1", tc.action, tc.enforce, resp.GetGateId())
			}
			continue
		}
		if resp.GetPaused() {
			t.Errorf("action=%q enforce=%v: paused=true, want not a hold", tc.action, tc.enforce)
		}

		if tc.wantActionTag == "" {
			if resp.GetErrorMsg() != "" {
				t.Errorf("action=%q enforce=%v: unexpected error %q", tc.action, tc.enforce, resp.GetErrorMsg())
			}
			continue
		}
		// The requested action must be reported verbatim so the audit trail stays
		// truthful even though the runtime effect collapses onto a refusal.
		if !strings.Contains(resp.GetErrorMsg(), "runtime gate "+tc.wantActionTag) {
			t.Errorf("action=%q enforce=%v: error %q should report action %q", tc.action, tc.enforce, resp.GetErrorMsg(), tc.wantActionTag)
		}
	}
}

// TestExecutorGateFailureIsOpenByDefault locks in the fail-open default: a gate
// that cannot decide must not turn into a runtime outage.
func TestExecutorGateFailureIsOpenByDefault(t *testing.T) {
	called := false
	gate := &errorGate{}
	executor := newDemoExecutor(&called).WithRuntimeGate(gate)

	resp := executor.Execute(context.Background(), demoRequest())

	if !called {
		t.Fatalf("fail-open default: handler should still run when the gate errors")
	}
	if !resp.GetSuccess() {
		t.Fatalf("fail-open default: expected success, got error %q", resp.GetErrorMsg())
	}
	if gate.calls != 1 {
		t.Fatalf("expected one gate call, got %d", gate.calls)
	}
}

func TestExecutorGateFailureClosed(t *testing.T) {
	called := false
	gate := &errorGate{}
	executor := newDemoExecutor(&called).WithRuntimeGate(gate).WithGateFailurePolicy(GateFailureClosed)

	resp := executor.Execute(context.Background(), demoRequest())

	if called {
		t.Fatalf("fail-closed: handler must not run when the gate errors")
	}
	if resp.GetSuccess() {
		t.Fatalf("fail-closed: expected failure")
	}
	if !strings.Contains(resp.GetErrorMsg(), "fail-closed") {
		t.Fatalf("error %q should mention the fail-closed policy", resp.GetErrorMsg())
	}
}

// TestExecutorGateOptionsCompose guards against an option setter dropping a
// previously configured one (WithRuntimeGate used to rebuild the executor).
func TestExecutorGateOptionsCompose(t *testing.T) {
	called := false
	gate := &errorGate{}

	// Policy first, then gate.
	executor := newDemoExecutor(&called).WithGateFailurePolicy(GateFailureClosed).WithRuntimeGate(gate)
	resp := executor.Execute(context.Background(), demoRequest())
	if called || resp.GetSuccess() {
		t.Fatalf("policy set before gate was lost: called=%v success=%v", called, resp.GetSuccess())
	}

	// Gate first, then policy.
	called = false
	executor = newDemoExecutor(&called).WithRuntimeGate(gate).WithGateFailurePolicy(GateFailureClosed)
	resp = executor.Execute(context.Background(), demoRequest())
	if called || resp.GetSuccess() {
		t.Fatalf("gate lost when policy set afterwards: called=%v success=%v", called, resp.GetSuccess())
	}
}

// TestExecutorUnknownActionDoesNotBypassGate checks that a gate returning a
// malformed action cannot be used to sneak past enforcement.
func TestExecutorUnknownActionDoesNotBypassGate(t *testing.T) {
	called := false
	gate := &testGate{decision: GateDecision{Action: "definitely-not-an-action", Reason: "typo", Enforce: true}}
	executor := newDemoExecutor(&called).WithRuntimeGate(gate)

	resp := executor.Execute(context.Background(), demoRequest())

	if called {
		t.Fatalf("unknown action must not let the handler run")
	}
	if resp.GetSuccess() {
		t.Fatalf("unknown action must fail safe")
	}
}
