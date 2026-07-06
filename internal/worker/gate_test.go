package worker

import (
	"context"
	"testing"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

type testGate struct {
	decision GateDecision
	calls    int
}

func (g *testGate) BeforeExecute(ctx context.Context, req GateRequest) (GateDecision, error) {
	g.calls++
	return g.decision, nil
}

func TestExecutorRuntimeGateShadowDoesNotChangeBehavior(t *testing.T) {
	registry := NewRegistry()
	registry.Register("demo", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	gate := &testGate{decision: GateDecision{Action: GateActionBlock, Reason: "shadow only", Enforce: false}}
	executor := NewExecutor(registry).WithRuntimeGate(gate)

	resp := executor.Execute(context.Background(), &forgev1.TaskRequest{TaskId: "task_1", WorkflowId: "wf_1", TaskName: "Demo", Handler: "demo"})
	if !resp.GetSuccess() {
		t.Fatalf("expected shadow gate to allow handler, got error %q", resp.GetErrorMsg())
	}
	if gate.calls != 1 {
		t.Fatalf("expected gate call, got %d", gate.calls)
	}
}

func TestExecutorRuntimeGateEnforceBlocksHandler(t *testing.T) {
	registry := NewRegistry()
	called := false
	registry.Register("demo", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		called = true
		return map[string]interface{}{"ok": true}, nil
	})
	gate := &testGate{decision: GateDecision{Action: GateActionBlock, Reason: "blocked by policy", Enforce: true}}
	executor := NewExecutor(registry).WithRuntimeGate(gate)

	resp := executor.Execute(context.Background(), &forgev1.TaskRequest{TaskId: "task_1", WorkflowId: "wf_1", TaskName: "Demo", Handler: "demo"})
	if resp.GetSuccess() {
		t.Fatalf("expected enforce gate to block")
	}
	if called {
		t.Fatalf("handler should not be called after enforce block")
	}
}
