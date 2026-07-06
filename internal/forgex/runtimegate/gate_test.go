package runtimegate

import (
	"context"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/policy"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/worker"
)

func TestGateShadowPersistsButDoesNotEnforce(t *testing.T) {
	store := storage.NewFileStore(t.TempDir())
	gate := New(Config{
		Mode:       model.GateModeShadow,
		Authority:  policy.AuthorityL0,
		Store:      store,
		CreateHITL: true,
		Now:        func() time.Time { return time.Date(2026, 7, 6, 7, 0, 0, 0, time.UTC) },
		Contracts: map[string]toolgw.ToolContract{
			"danger": {Name: "danger", RiskLevel: toolgw.RiskHigh, SideEffect: toolgw.SideEffectExternalWrite, ApprovalRequired: true},
		},
	})

	decision, err := gate.BeforeExecute(context.Background(), worker.GateRequest{TaskID: "task_1", WorkflowID: "run_1", Handler: "danger"})
	if err != nil {
		t.Fatalf("BeforeExecute() error = %v", err)
	}
	if decision.Enforce {
		t.Fatalf("shadow gate must not enforce")
	}
	if decision.Action != worker.GateActionPause {
		t.Fatalf("expected pause decision, got %s", decision.Action)
	}
}

func TestGateEnforceBlocksHighRiskTool(t *testing.T) {
	gate := New(Config{
		Mode:      model.GateModeEnforce,
		Authority: policy.AuthorityL0,
		Contracts: map[string]toolgw.ToolContract{
			"danger": {Name: "danger", RiskLevel: toolgw.RiskHigh, SideEffect: toolgw.SideEffectExternalWrite, ApprovalRequired: true},
		},
	})

	decision, err := gate.BeforeExecute(context.Background(), worker.GateRequest{TaskID: "task_1", WorkflowID: "run_1", Handler: "danger"})
	if err != nil {
		t.Fatalf("BeforeExecute() error = %v", err)
	}
	if !decision.Enforce {
		t.Fatalf("enforce gate should set Enforce=true")
	}
	if decision.Action != worker.GateActionPause {
		t.Fatalf("expected pause decision, got %s", decision.Action)
	}
}
