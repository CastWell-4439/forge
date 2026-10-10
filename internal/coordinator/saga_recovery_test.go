package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

const sagaWorkflowYAML = `
name: saga_recover
tasks:
  first:
    handler: shell
    compensate: undo_first
  second:
    handler: shell
    depends_on: [first]
    on_failure: COMPENSATE
`

// newSagaCoordinator builds a coordinator whose DAG cache is empty, which is the
// state after a restart.
func newSagaCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "saga.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return NewCoordinator(store)
}

// The submission event must carry the DAG, because that is the only place it
// survives a restart: the workflow row has no DefID written, so the definition
// table cannot be looked up, and the in-memory cache is gone.
func TestSubmissionEventCarriesTheDAG(t *testing.T) {
	coord := newSagaCoordinator(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: sagaWorkflowYAML,
	})
	require.NoError(t, err)

	events, err := coord.store.GetWorkflowHistory(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.NotEmpty(t, events)

	var submitted *storage.Event
	for _, e := range events {
		if e.Type == storage.EventWorkflowSubmitted {
			submitted = e
			break
		}
	}
	require.NotNil(t, submitted, "a submission event must be recorded")

	require.NotEmpty(t, submitted.Payload,
		"the event must carry the DAG: it is the only record that survives a restart")

	var payload struct {
		DagYAML string `json:"dag_yaml"`
	}
	require.NoError(t, json.Unmarshal(submitted.Payload, &payload))
	assert.Contains(t, payload.DagYAML, "saga_recover")
	assert.Contains(t, payload.DagYAML, "undo_first", "the compensation handler must survive too")
	assert.Contains(t, payload.DagYAML, "COMPENSATE", "and the policy that needs it")
}

// The whole point: a DAG the cache has lost is recovered from the event log.
//
// Before this, both the decision to compensate and the plan of what to roll back
// read memory alone, so a restart between the failure and the compensation made
// the coordinator conclude that nothing needed compensating — and a half-applied
// workflow stood.
func TestResolveDAGRecoversFromTheEventLog(t *testing.T) {
	coord := newSagaCoordinator(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: sagaWorkflowYAML,
	})
	require.NoError(t, err)
	workflowID := resp.GetWorkflowId()

	// Simulate a restart: the cache is empty, the store is not.
	coord.evictDAGCache(workflowID)
	assert.Nil(t, coord.cachedDAG(workflowID), "the cache must be empty for this to mean anything")

	recovered := coord.resolveDAG(context.Background(), workflowID)
	require.NotNil(t, recovered, "the DAG must come back from the event log")
	assert.Equal(t, "saga_recover", recovered.Name)

	second, ok := recovered.Tasks["second"]
	require.True(t, ok)
	assert.Equal(t, FailureActionCompensate, second.OnFailure,
		"the policy that decides whether to compensate must survive the round trip")

	first, ok := recovered.Tasks["first"]
	require.True(t, ok)
	assert.Equal(t, "undo_first", first.Compensate,
		"and so must the handler that does the rollback")
}

// The recovered DAG is cached, because a compensated workflow is looked at more
// than once (the decision, then the plan).
func TestResolveDAGCachesWhatItRecovers(t *testing.T) {
	coord := newSagaCoordinator(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: sagaWorkflowYAML,
	})
	require.NoError(t, err)
	workflowID := resp.GetWorkflowId()
	coord.evictDAGCache(workflowID)

	first := coord.resolveDAG(context.Background(), workflowID)
	require.NotNil(t, first)
	assert.Same(t, first, coord.cachedDAG(workflowID),
		"the recovered DAG must be put back, not re-read on every question")
}

// The cache is still the first place looked at: a live workflow must not pay for
// an event-log read on every failure question.
func TestResolveDAGPrefersTheCache(t *testing.T) {
	coord := newSagaCoordinator(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: sagaWorkflowYAML,
	})
	require.NoError(t, err)

	cached := coord.cachedDAG(resp.GetWorkflowId())
	require.NotNil(t, cached)
	assert.Same(t, cached, coord.resolveDAG(context.Background(), resp.GetWorkflowId()))
}

// shouldCompensate now survives a restart: it answers from the recovered DAG
// rather than defaulting to "no".
func TestShouldCompensateSurvivesARestart(t *testing.T) {
	coord := newSagaCoordinator(t)

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: sagaWorkflowYAML,
	})
	require.NoError(t, err)
	workflowID := resp.GetWorkflowId()
	coord.evictDAGCache(workflowID)

	assert.True(t, coord.shouldCompensate(context.Background(), workflowID, "second"),
		"a task declaring COMPENSATE must still be recognised after a restart")

	// A task that did not declare it is still not compensated.
	assert.False(t, coord.shouldCompensate(context.Background(), workflowID, "first"))

	// An unknown task is not compensated either.
	assert.False(t, coord.shouldCompensate(context.Background(), workflowID, "no-such-task"))
}

// A workflow with no recoverable DAG is reported as such, and answered "do not
// compensate" — the same behaviour as before, but now for a reason that is
// visible in the log rather than by silent default.
func TestResolveDAGWithoutAPayloadIsNil(t *testing.T) {
	coord := newSagaCoordinator(t)

	// A workflow written directly, with no submission event carrying a DAG.
	require.NoError(t, coord.store.SaveWorkflow(context.Background(), &storage.Workflow{
		ID:     "wf-no-payload",
		Name:   "manual",
		Status: storage.WorkflowStatusRunning,
	}))

	assert.Nil(t, coord.resolveDAG(context.Background(), "wf-no-payload"))
	assert.False(t, coord.shouldCompensate(context.Background(), "wf-no-payload", "any"))
}

// An unparseable payload does not take the coordinator down: it is reported and
// treated as "cannot tell", which means "do not compensate".
func TestResolveDAGSurvivesACorruptPayload(t *testing.T) {
	coord := newSagaCoordinator(t)

	require.NoError(t, coord.store.SaveWorkflow(context.Background(), &storage.Workflow{
		ID:     "wf-corrupt",
		Name:   "corrupt",
		Status: storage.WorkflowStatusRunning,
	}))
	coord.saveEvent(context.Background(), "wf-corrupt", "", storage.EventWorkflowSubmitted,
		json.RawMessage(`{"dag_yaml":"this is not: [valid yaml"}`))

	assert.Nil(t, coord.resolveDAG(context.Background(), "wf-corrupt"))
}

// The DAG that comes back is the one that was submitted: round-tripping through
// the event must not lose declarations, or compensation would plan against a
// different workflow than the one that ran.
func TestRecoveredDAGKeepsEveryDeclaration(t *testing.T) {
	coord := newSagaCoordinator(t)

	originalYAML := `
name: full_round_trip
timeout: 30m
tasks:
  a:
    handler: shell
    params:
      command: "go test ./..."
    output: first
    timeout: 5m
    retry:
      max_attempts: 3
      backoff: exponential
      initial_interval: 2s
    compensate: undo_a
  b:
    handler: shell
    depends_on: [a]
    condition: "results.first.ok"
    on_failure: COMPENSATE
    compensate: undo_b
    on_result:
      success: continue
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: originalYAML,
	})
	require.NoError(t, err)
	workflowID := resp.GetWorkflowId()
	coord.evictDAGCache(workflowID)

	recovered := coord.resolveDAG(context.Background(), workflowID)
	require.NotNil(t, recovered)

	assert.Equal(t, "full_round_trip", recovered.Name)
	require.Len(t, recovered.Tasks, 2)

	a, ok := recovered.Tasks["a"]
	require.True(t, ok)
	assert.Equal(t, "shell", a.Handler)
	assert.Equal(t, "first", a.Output)
	assert.Equal(t, "undo_a", a.Compensate)
	assert.Equal(t, 3, a.Retry.MaxAttempts, "the retry policy must survive")
	assert.Equal(t, "go test ./...", a.Params["command"], "params must survive")

	b, ok := recovered.Tasks["b"]
	require.True(t, ok)
	assert.Equal(t, FailureActionCompensate, b.OnFailure)
	assert.Equal(t, "undo_b", b.Compensate)
	assert.Equal(t, []string{"a"}, b.DependsOn)
	assert.Equal(t, "results.first.ok", b.Condition, "the condition must survive")

	// And the recovered DAG is still a valid DAG.
	assert.NoError(t, recovered.Validate())
}
