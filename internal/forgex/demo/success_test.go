package demo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/forgex/demo"
	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// repoPath resolves a repo-root-relative path from the demo package directory so
// the test can load the real ForgeX configs and examples.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatalf("resolve %s: %v", rel, err)
	}
	return abs
}

func TestRunGenericContractSuccessDemo(t *testing.T) {
	root := t.TempDir()
	taxonomy := repoPath(t, "configs/forgex/failure_taxonomy.yaml")
	policy := repoPath(t, "configs/forgex/stop_policies.yaml")
	packet := repoPath(t, "examples/forgex/task_packet_generic_contract_success.yaml")
	contracts := repoPath(t, "configs/forgex/tool_contracts/generic_tool_contracts.yaml")
	toolPolicy := repoPath(t, "configs/forgex/policies/safe_default.yaml")

	runID, err := demo.RunGenericContractSuccessDemoWithControl(context.Background(), root, taxonomy, policy, packet, contracts, toolPolicy, "")
	if err != nil {
		t.Fatalf("RunGenericContractSuccessDemoWithControl() error = %v", err)
	}
	runDir := filepath.Join(root, "runs", runID)

	// The run must complete successfully, not stop.
	var run model.Run
	if data, err := os.ReadFile(filepath.Join(runDir, "run.json")); err != nil {
		t.Fatalf("read run.json: %v", err)
	} else if err := json.Unmarshal(data, &run); err != nil {
		t.Fatalf("decode run.json: %v", err)
	}
	if run.Status != model.RunSucceeded {
		t.Fatalf("run status = %s, want %s", run.Status, model.RunSucceeded)
	}

	// The report must have been written.
	if _, err := os.Stat(filepath.Join(runDir, "report.md")); err != nil {
		t.Fatalf("report.md missing: %v", err)
	}

	// The happy-path eval suite must pass end to end.
	result, err := forgexeval.Run(context.Background(), runDir, repoPath(t, "configs/forgex/eval_rules.yaml"), "generic_contract_happy_v1")
	if err != nil {
		t.Fatalf("eval Run() error = %v", err)
	}
	if result.Status != model.EvalPassed {
		t.Fatalf("eval status = %s, want %s", result.Status, model.EvalPassed)
	}

	// Control metrics must not false-positive on a clean run.
	idx, err := storage.OpenSQLiteIndex(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteIndex() error = %v", err)
	}
	defer idx.Close()
	if err := idx.IndexRunDir(context.Background(), runDir); err != nil {
		t.Fatalf("IndexRunDir() error = %v", err)
	}
	runs, err := idx.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	var found bool
	for _, r := range runs {
		if r.ID != runID {
			continue
		}
		found = true
		m := r.Metrics
		if m.PolicyDecisionCount != 1 {
			t.Errorf("policy_decision_count = %d, want 1", m.PolicyDecisionCount)
		}
		if m.PolicyDenyCount != 0 || m.ApprovalRequiredCount != 0 {
			t.Errorf("deny=%d approval=%d, want 0/0", m.PolicyDenyCount, m.ApprovalRequiredCount)
		}
		if m.ContractValidationFailedCount != 0 {
			t.Errorf("contract_validation_failed_count = %d, want 0", m.ContractValidationFailedCount)
		}
		if m.MissingArtifactCount != 0 {
			t.Errorf("missing_artifact_count = %d, want 0", m.MissingArtifactCount)
		}
		if m.StateConflictCount != 0 {
			t.Errorf("state_conflict_count = %d, want 0", m.StateConflictCount)
		}
		if m.SafeStopCount != 0 || m.PauseCount != 0 {
			t.Errorf("safe_stop=%d pause=%d, want 0/0", m.SafeStopCount, m.PauseCount)
		}
	}
	if !found {
		t.Fatalf("run %s not found in index", runID)
	}
}
