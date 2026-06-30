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

func TestEnginePaidCallBelowL3Denies(t *testing.T) {
	contract := lowRiskContract()
	contract.SideEffect = toolgw.SideEffectPaidCall
	decision := NewEngine(nil).Decide("run-1", AuthorityL2, contract)
	if decision.Action != ActionDeny {
		t.Fatalf("expected paid call deny, got %+v", decision)
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
	return toolgw.ToolContract{Name: "vidu.reference2video", Capability: "video_generation", RiskLevel: toolgw.RiskHigh, SideEffect: toolgw.SideEffectExternalAPICall}
}

func lowRiskContract() toolgw.ToolContract {
	return toolgw.ToolContract{Name: "local.inspect", Capability: "inspect", RiskLevel: toolgw.RiskLow, SideEffect: toolgw.SideEffectReadOnly}
}
