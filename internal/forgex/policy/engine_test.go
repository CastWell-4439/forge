package policy

import (
	"os"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/toolgw"
)

func TestEngineFirstMatchWins(t *testing.T) {
	cfg := &Config{Rules: []Rule{
		{ID: "first", When: Condition{RiskLevel: "high"}, Action: ActionDeny, Reason: "first wins"},
		{ID: "second", When: Condition{RiskLevel: "high"}, Action: ActionAllow, Reason: "second loses"},
	}}
	decision := NewEngine(cfg).Decide("run-1", AuthorityL4, highRiskContract())
	if decision.Action != ActionDeny || decision.Reason != "first wins" {
		t.Fatalf("unexpected decision %+v", decision)
	}
}

func TestEngineAuthorityBelowRequiredDenies(t *testing.T) {
	contract := highRiskContract()
	contract.RiskLevel = toolgw.RiskLow
	contract.RequiredAuthorityLevel = "L3"
	decision := NewEngine(nil).Decide("run-1", AuthorityL2, contract)
	if decision.Action != ActionDeny || !strings.Contains(decision.Reason, "below required") {
		t.Fatalf("expected deny below required, got %+v", decision)
	}
}

func TestEngineApprovalRequired(t *testing.T) {
	contract := lowRiskContract()
	contract.ApprovalRequired = true
	decision := NewEngine(nil).Decide("run-1", AuthorityL4, contract)
	if decision.Action != ActionRequireApproval || !decision.RequiresHITL {
		t.Fatalf("expected approval required, got %+v", decision)
	}
}

func TestEngineHighRiskFallbackRequiresApproval(t *testing.T) {
	decision := NewEngine(nil).Decide("run-1", AuthorityL4, highRiskContract())
	if decision.Action != ActionRequireApproval || !decision.RequiresHITL {
		t.Fatalf("expected high risk approval, got %+v", decision)
	}
}

func TestEngineSafeReadOnlyAllows(t *testing.T) {
	decision := NewEngine(nil).Decide("run-1", AuthorityL2, lowRiskContract())
	if decision.Action != ActionAllow {
		t.Fatalf("expected allow, got %+v", decision)
	}
}

func TestEngineL1ExternalAPICallDenies(t *testing.T) {
	contract := lowRiskContract()
	contract.SideEffect = toolgw.SideEffectExternalAPICall
	decision := NewEngine(nil).Decide("run-1", AuthorityL1, contract)
	if decision.Action != ActionDeny {
		t.Fatalf("expected L1 external api call deny, got %+v", decision)
	}
}

func TestEngineL2LowRiskReadOnlyAllows(t *testing.T) {
	decision := NewEngine(nil).Decide("run-1", AuthorityL2, lowRiskContract())
	if decision.Action != ActionAllow || decision.Authority != AuthorityL2 {
		t.Fatalf("expected L2 low risk allow with authority recorded, got %+v", decision)
	}
}

func TestEnginePaidCallBelowL3Denies(t *testing.T) {
	contract := lowRiskContract()
	contract.SideEffect = toolgw.SideEffectPaidCall
	decision := NewEngine(nil).Decide("run-1", AuthorityL2, contract)
	if decision.Action != ActionDeny {
		t.Fatalf("expected paid call deny, got %+v", decision)
	}
}

func TestEngineL3HighRiskApprovalRequired(t *testing.T) {
	contract := highRiskContract()
	contract.ApprovalRequired = true
	decision := NewEngine(nil).Decide("run-1", AuthorityL3, contract)
	if decision.Action != ActionRequireApproval || !decision.RequiresHITL || decision.Authority != AuthorityL3 {
		t.Fatalf("expected L3 high risk approval, got %+v", decision)
	}
}

func TestEngineAuthorityAtLeastCondition(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "allow-l2-plus",
		When:   Condition{ToolName: "demo.expensive_generation", AuthorityAtLeast: "L2"},
		Action: ActionAllow,
	}}}
	contract := highRiskContract()
	if got := NewEngine(cfg).Decide("run-1", AuthorityL1, contract); got.Action == ActionAllow {
		t.Fatalf("expected L1 not to match authority_at_least rule, got %+v", got)
	}
	if got := NewEngine(cfg).Decide("run-1", AuthorityL2, contract); got.Action != ActionAllow {
		t.Fatalf("expected L2 to match authority_at_least rule, got %+v", got)
	}
}

func TestLoadConfigMissingVersion(t *testing.T) {
	// Covered by parser path through a temp file to keep the public function tested.
	path := t.TempDir() + "/policy.yaml"
	if err := os.WriteFile(path, []byte("profile: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func highRiskContract() toolgw.ToolContract {
	return toolgw.ToolContract{Name: "demo.expensive_generation", Capability: "generation.expensive", RiskLevel: toolgw.RiskHigh, SideEffect: toolgw.SideEffectExternalAPICall}
}

func lowRiskContract() toolgw.ToolContract {
	return toolgw.ToolContract{Name: "local.inspect", Capability: "inspect", RiskLevel: toolgw.RiskLow, SideEffect: toolgw.SideEffectReadOnly}
}
