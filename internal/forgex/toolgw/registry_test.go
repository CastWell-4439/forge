package toolgw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContractsSuccess(t *testing.T) {
	path := writeContractConfig(t, `version: 1
tools:
  - name: demo.expensive_generation
    capability: generation.expensive
    risk_level: high
    side_effect: external_api_call
`)
	registry, err := LoadContracts(path)
	if err != nil {
		t.Fatalf("LoadContracts() error = %v", err)
	}
	contract, ok := registry.Get("demo.expensive_generation")
	if !ok {
		t.Fatalf("expected contract")
	}
	if contract.Capability != "generation.expensive" {
		t.Fatalf("unexpected capability %q", contract.Capability)
	}
}

func TestLoadContractsMissingVersion(t *testing.T) {
	path := writeContractConfig(t, `tools:
  - name: tool
    capability: read
    risk_level: low
    side_effect: read_only
`)
	_, err := LoadContracts(path)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestNewRegistryRejectsDuplicateName(t *testing.T) {
	_, err := NewRegistry([]ToolContract{
		{Name: "tool", Capability: "read", RiskLevel: RiskLow, SideEffect: SideEffectReadOnly},
		{Name: "tool", Capability: "write", RiskLevel: RiskMedium, SideEffect: SideEffectLocalWrite},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestNewRegistryRejectsUnknownRisk(t *testing.T) {
	_, err := NewRegistry([]ToolContract{{Name: "tool", Capability: "read", RiskLevel: "weird", SideEffect: SideEffectReadOnly}})
	if err == nil || !strings.Contains(err.Error(), "unknown risk") {
		t.Fatalf("expected unknown risk error, got %v", err)
	}
}

func TestNewRegistryRejectsUnknownSideEffect(t *testing.T) {
	_, err := NewRegistry([]ToolContract{{Name: "tool", Capability: "read", RiskLevel: RiskLow, SideEffect: "networkish"}})
	if err == nil || !strings.Contains(err.Error(), "unknown side_effect") {
		t.Fatalf("expected unknown side effect error, got %v", err)
	}
}

func TestMustGetMissing(t *testing.T) {
	registry, err := NewRegistry([]ToolContract{{Name: "tool", Capability: "read", RiskLevel: RiskLow, SideEffect: SideEffectReadOnly}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.MustGet("missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func writeContractConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contracts.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
