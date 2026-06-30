package context

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("abcd"); got != 1 {
		t.Fatalf("EstimateTokens(abcd) = %d, want 1", got)
	}
	if got := EstimateTokens("abcde"); got != 2 {
		t.Fatalf("EstimateTokens(abcde) = %d, want 2", got)
	}
}

func TestBudgetManagerBuildWithinBudget(t *testing.T) {
	manager := NewBudgetManager(10)
	pack := manager.Build("run_1", "summarize", "short context", []string{"artifact_1"})
	if pack.RunID != "run_1" || pack.Purpose != "summarize" {
		t.Fatalf("pack identity mismatch: %+v", pack)
	}
	if pack.BudgetExceeded {
		t.Fatalf("BudgetExceeded = true, want false")
	}
	if pack.Truncated {
		t.Fatalf("Truncated = true, want false")
	}
	if len(pack.ArtifactRefs) != 1 || pack.ArtifactRefs[0] != "artifact_1" {
		t.Fatalf("ArtifactRefs mismatch: %+v", pack.ArtifactRefs)
	}
}

func TestBudgetManagerBuildOverBudget(t *testing.T) {
	manager := NewBudgetManager(2)
	pack := manager.Build("run_1", "large", strings.Repeat("x", 100), nil)
	if !pack.BudgetExceeded {
		t.Fatalf("BudgetExceeded = false, want true")
	}
	if !pack.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if pack.Metadata["warning"] != "context_budget_exceeded" {
		t.Fatalf("warning metadata mismatch: %+v", pack.Metadata)
	}
}
