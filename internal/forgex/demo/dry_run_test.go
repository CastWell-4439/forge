package demo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/forgex/demo"
	"github.com/castwell/forge/internal/forgex/model"
	forgexpolicy "github.com/castwell/forge/internal/forgex/policy"
)

// A dry-run decision keeps the validation half and drops the side effect. This
// pins the semantics at the decision level, where the demo reads them.
func TestDryRunDecisionDoesNotExecute(t *testing.T) {
	action := forgexpolicy.ActionDryRunOnly

	assert.False(t, action.AllowsExecution(), "a dry run must not execute the tool")
	assert.Equal(t, forgexpolicy.ExecutionDryRun, action.Mode())
	assert.Equal(t, model.StopActionContinue, action.StopAction(),
		"a dry run does not halt the run; it only withholds the side effect")
}

// The demo path still runs its contract validation for a dry run: that is the
// "validation only" half of the mode, and the recorded args carry the mode so
// the report cannot be misread as a real call.
func TestDryRunRunReachesValidation(t *testing.T) {
	root := t.TempDir()
	runID, err := demo.RunScenario(context.Background(), demo.ScenarioConfig{
		Root:           root,
		TaxonomyPath:   repoPath(t, "configs/forgex/failure_taxonomy.yaml"),
		PolicyPath:     repoPath(t, "configs/forgex/stop_policies.yaml"),
		PacketPath:     repoPath(t, "examples/forgex/task_packet_generic_contract_success.yaml"),
		ContractsPath:  repoPath(t, "configs/forgex/tool_contracts/generic_tool_contracts.yaml"),
		ToolPolicyPath: repoPath(t, "configs/forgex/policies/safe_default.yaml"),
	})
	require.NoError(t, err)

	// The allow path is unaffected by the dry-run plumbing: it still succeeds
	// and its recorded args carry the explicit execution mode.
	run := readRun(t, root, runID)
	assert.Equal(t, model.RunSucceeded, run.Status)

	calls, err := os.ReadFile(filepath.Join(root, "runs", runID, "tool_calls.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, calls)
	assert.Contains(t, string(calls), "forge_execution_mode",
		"the recorded call must state which execution mode authorized it")
	assert.Contains(t, string(calls), string(forgexpolicy.ExecutionProceed))
}
