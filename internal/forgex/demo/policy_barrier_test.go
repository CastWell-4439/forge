package demo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/forgex/demo"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// runDeniedDemo replays the low-authority packet through the real pipeline.
func runDeniedDemo(t *testing.T) (root, runID string) {
	t.Helper()
	root = t.TempDir()

	var err error
	runID, err = demo.RunScenario(context.Background(), demo.ScenarioConfig{
		Root:           root,
		TaxonomyPath:   repoPath(t, "configs/forgex/failure_taxonomy.yaml"),
		PolicyPath:     repoPath(t, "configs/forgex/stop_policies.yaml"),
		PacketPath:     repoPath(t, "examples/forgex/task_packet_generic_policy_denied.yaml"),
		ContractsPath:  repoPath(t, "configs/forgex/tool_contracts/generic_tool_contracts.yaml"),
		ToolPolicyPath: repoPath(t, "configs/forgex/policies/safe_default.yaml"),
	})
	require.NoError(t, err)
	return root, runID
}

func readRun(t *testing.T, root, runID string) model.Run {
	t.Helper()
	var run model.Run
	data, err := os.ReadFile(filepath.Join(root, "runs", runID, "run.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &run))
	return run
}

// The core of the fix: a denied call must not run, and the run must say so
// instead of continuing as if it had permission.
func TestPolicyDeniedRunStopsWithoutExecuting(t *testing.T) {
	root, runID := runDeniedDemo(t)
	runDir := filepath.Join(root, "runs", runID)

	// 1. The run is stopped, not succeeded and not paused.
	run := readRun(t, root, runID)
	assert.Equal(t, model.RunStopped, run.Status, "a denied call stops the run")
	assert.Contains(t, run.Summary, "stop")

	// 2. No tool call was executed: the simulation never started.
	_, err := os.Stat(filepath.Join(runDir, "tool_calls.jsonl"))
	if err == nil {
		data, readErr := os.ReadFile(filepath.Join(runDir, "tool_calls.jsonl"))
		require.NoError(t, readErr)
		assert.NotContains(t, string(data), "started",
			"a denied call must never be recorded as started")
	}

	// 3. No artifact: nothing was produced because nothing ran.
	artifacts, err := os.ReadFile(filepath.Join(runDir, "artifacts.jsonl"))
	if err == nil {
		assert.NotContains(t, string(artifacts), string(model.ArtifactProduced),
			"a denied call produces no artifact")
	}

	// 4. The refusal is classified and recorded, not merely logged. An
	// unclassified refusal ("unknown") would read as a mystery error.
	errorsData, err := os.ReadFile(filepath.Join(runDir, "errors.jsonl"))
	require.NoError(t, err, "a denial must leave an error envelope")
	assert.Contains(t, string(errorsData), "policy_decision")
	assert.Contains(t, string(errorsData), "policy_violation",
		"the denial must classify through the taxonomy, not land in \"unknown\"")
	assert.Contains(t, string(errorsData), "POLICY_DENIED_TOOL_CALL")

	// 5. The stop signal came from the policy decision and decided stop.
	signals, err := os.ReadFile(filepath.Join(runDir, "stop_signals.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(signals), "policy_decision")
	decisions, err := os.ReadFile(filepath.Join(runDir, "stop_decisions.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(decisions), string(model.StopActionStop),
		"a deny must arbitrate to stop, never to continue")
}

// The ledger must describe what really happened. The old path wrote a fixed
// "passed the policy decision" line no matter what the decision said.
func TestPolicyDeniedLedgerReflectsRefusal(t *testing.T) {
	root, runID := runDeniedDemo(t)

	data, err := os.ReadFile(filepath.Join(root, "runs", runID, "progress_ledger.yaml"))
	require.NoError(t, err)
	var ledger model.ProgressLedger
	require.NoError(t, yaml.Unmarshal(data, &ledger))

	var policyItem *model.ProgressItem
	for i := range ledger.Checklist {
		if ledger.Checklist[i].ID == "policy_check" {
			policyItem = &ledger.Checklist[i]
		}
	}
	require.NotNil(t, policyItem)
	assert.Equal(t, model.ProgressBlocked, policyItem.Status, "the authorize step is blocked, not done")
	assert.Contains(t, policyItem.Evidence, "deny")

	assert.Contains(t, ledger.CurrentPhase, "policy")
	assert.NotEmpty(t, ledger.Blockers, "a refusal must record a blocker")
	assert.NotEmpty(t, ledger.NextActions, "a refusal must say what to do next")

	// The hardcoded claim must be gone.
	joined := ""
	for _, d := range ledger.Decisions {
		joined += d + "\n"
	}
	assert.NotContains(t, joined, "passed the policy decision",
		"the ledger must not assert a policy pass that did not happen")
	assert.Contains(t, joined, "not executed")
}

// The control metrics must show the denial, so a dashboard/scorecard surfaces
// it rather than reading the run as a clean one.
func TestPolicyDeniedRunIsVisibleInMetrics(t *testing.T) {
	root, runID := runDeniedDemo(t)

	idx, err := storage.OpenSQLiteIndex(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	defer idx.Close()
	require.NoError(t, idx.IndexRunDir(context.Background(), filepath.Join(root, "runs", runID)))

	runs, err := idx.ListRuns(context.Background(), 10)
	require.NoError(t, err)
	var found bool
	for _, r := range runs {
		if r.ID != runID {
			continue
		}
		found = true
		assert.Equal(t, 1, r.Metrics.PolicyDecisionCount)
		assert.Equal(t, 1, r.Metrics.PolicyDenyCount, "the denial must be counted")
		assert.Equal(t, 1, r.Metrics.SafeStopCount, "the run stopped safely, not silently")
	}
	assert.True(t, found, "the denied run must be indexed")
}

// A report is still written for a refused run: the audit trail of "what we
// tried and why it was not allowed" is the point.
func TestPolicyDeniedRunWritesReportAndBadCase(t *testing.T) {
	root, runID := runDeniedDemo(t)
	runDir := filepath.Join(root, "runs", runID)

	reportData, err := os.ReadFile(filepath.Join(runDir, "report.md"))
	require.NoError(t, err, "a refused run still produces a report")
	assert.Contains(t, string(reportData), "deny")

	_, err = os.Stat(filepath.Join(runDir, "badcase.yaml"))
	assert.NoError(t, err, "a refusal is a bad case worth promoting")
}

// The allow path must be untouched: the existing success demo still succeeds.
// This is the regression guard for the branch added above.
func TestAllowingRunStillSucceeds(t *testing.T) {
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
	assert.Equal(t, model.RunSucceeded, readRun(t, root, runID).Status)
}
