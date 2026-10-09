package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// routeTestSetup wires one worker that always succeeds.
func routeTestSetup(t *testing.T) (*Coordinator, *countWorker) {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "route.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	worker := newCountWorker("")
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{
		ID:       "w-shell",
		Handlers: []string{"shell"},
		Capacity: 10,
		Client:   worker,
	}
	coord.mu.Unlock()
	return coord, worker
}

// on_result: abort ends the workflow as soon as the task succeeds, even though
// other tasks remain. The task itself is COMPLETED — it succeeded; the run was
// told to stop.
func TestOnResultAbortStopsTheWorkflow(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: route_abort
tasks:
  gate:
    handler: shell
    on_result:
      success: abort
  never:
    handler: shell
    depends_on: [gate]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond, "abort must fail the workflow")

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusCompleted, statuses["gate"], "the task that asked to abort did succeed")
	assert.NotEqual(t, storage.TaskStatusCompleted, statuses["never"], "the downstream task must not run")

	assert.Equal(t, 1, worker.count(), "only the gate task reached a worker")
}

// on_result: skip lets the task succeed but prevents its downstream from
// running. Unlike abort, the workflow can still complete.
func TestOnResultSkipLeavesDownstreamUnrun(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: route_skip
tasks:
  gate:
    handler: shell
    on_result:
      success: skip
  never:
    handler: shell
    depends_on: [gate]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond,
		"a skip leaves the workflow completable, unlike abort")

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusCompleted, statuses["gate"])
	assert.Equal(t, storage.TaskStatusSkipped, statuses["never"], "the downstream must be settled as skipped")

	assert.Equal(t, 1, worker.count(), "the skipped task must not reach a worker")
}

// The skip settles everything reachable, not just the immediate successor: a
// grandchild whose only parent was skipped can never become READY, and leaving
// it PENDING would hold the workflow open forever.
func TestOnResultSkipSettlesTheWholeSubtree(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: route_skip_deep
tasks:
  gate:
    handler: shell
    on_result:
      success: skip
  child:
    handler: shell
    depends_on: [gate]
  grandchild:
    handler: shell
    depends_on: [child]
  unrelated:
    handler: shell
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond)

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusCompleted, statuses["gate"])
	assert.Equal(t, storage.TaskStatusSkipped, statuses["child"], "the direct successor is settled")
	assert.Equal(t, storage.TaskStatusSkipped, statuses["grandchild"], "and so is its child")
	assert.Equal(t, storage.TaskStatusCompleted, statuses["unrelated"], "an independent task still runs")

	assert.Equal(t, 2, worker.count(), "only gate and the unrelated task reach a worker")
}

// A route declared for another status does not affect success: the default is
// still to continue.
func TestOnResultForAnotherStatusDoesNotAffectSuccess(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: route_other_status
tasks:
  a:
    handler: shell
    on_result:
      failure: abort
  b:
    handler: shell
    depends_on: [a]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond, "a failure rule must not fire on success")

	assert.Equal(t, 2, worker.count(), "both tasks run normally")
}

// A goto to a task that is not an ancestor cannot be honoured, and the refusal
// fails the run rather than being dropped. Failing matters: the jumping task is
// already COMPLETED, so without an explicit verdict nothing would advance past
// it and the run would sit unfinished with only a log line to explain why.
//
// The goto itself is covered in depth in goto_loop_test.go; this keeps the
// routing table's own view of it, where "b" is a descendant so the refusal is
// the only thing in flight.
func TestOnResultGotoToNonAncestorFailsTheRun(t *testing.T) {
	coord, _ := routeTestSetup(t)

	dagYAML := `
name: route_goto
tasks:
  a:
    handler: shell
    on_result:
      success: "goto:b"
  b:
    handler: shell
    depends_on: [a]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond,
		"an unhonourable route must fail the run instead of stalling it")

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	for _, task := range tasks {
		if task.TaskName == "b" {
			assert.Equal(t, storage.TaskStatusPending, task.Status,
				"goto must not be silently treated as continue")
		}
	}
}

// Without any on_result block the historical behaviour stands: continue.
func TestNoOnResultContinues(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: route_none
tasks:
  a:
    handler: shell
  b:
    handler: shell
    depends_on: [a]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond)

	assert.Equal(t, 2, worker.count())
}

// --- resolution table ---

func TestOnResultResolveDefaultsToContinue(t *testing.T) {
	var nilRoutes OnResult
	assert.Equal(t, RouteActionContinue, nilRoutes.Resolve("success").Action,
		"a nil map resolves to continue")

	routes, err := ParseOnResult(map[string]any{
		"success": "abort",
		"failure": "skip",
	})
	require.NoError(t, err)
	assert.Equal(t, RouteActionAbort, routes.Resolve("success").Action)
	assert.Equal(t, RouteActionSkip, routes.Resolve("failure").Action)
	assert.Equal(t, RouteActionContinue, routes.Resolve("timeout").Action,
		"an undeclared status continues")
}

// A skip must survive a concurrent completion.
//
// This is the guard for a real defect found while wiring on_result: eligibility
// used to be mutated as a side effect of each task finishing, so when an
// unrelated task finished at the same moment as the skipping task, its handler
// could mark the skipped branch READY — and whether the skip held depended on
// goroutine scheduling. The fix derives eligibility from persisted state, so the
// outcome no longer depends on order. Repeating the scenario is what makes this
// test able to catch a regression: a single run can pass by luck.
func TestSkipHoldsWhenAnotherTaskFinishesConcurrently(t *testing.T) {
	for i := 0; i < 8; i++ {
		coord, worker := routeTestSetup(t)

		// gate and unrelated are both roots, so they are dispatched together;
		// child can only be reached through gate.
		dagYAML := `
name: route_skip_race
tasks:
  gate:
    handler: shell
    on_result:
      success: skip
  child:
    handler: shell
    depends_on: [gate]
  unrelated:
    handler: shell
`
		resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
			return err == nil && wf.Status == storage.WorkflowStatusCompleted
		}, 20*time.Second, 25*time.Millisecond, "iteration %d: the workflow must settle", i)

		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		assert.Equal(t, storage.TaskStatusCompleted, statuses["gate"], "iteration %d", i)
		assert.Equal(t, storage.TaskStatusCompleted, statuses["unrelated"], "iteration %d", i)
		assert.Equal(t, storage.TaskStatusSkipped, statuses["child"],
			"iteration %d: the skipped branch must stay skipped regardless of finish order", i)

		assert.Equal(t, 2, worker.count(),
			"iteration %d: only the two roots may reach a worker", i)
	}
}

// The two kinds of skip mean different things, and conflating them would make
// one of them wrong. A task excluded by its own condition satisfies its
// dependents (they still run); a task routed to skip stops them.
func TestConditionSkipAndRouteSkipDiffer(t *testing.T) {
	coord, worker := routeTestSetup(t)

	dagYAML := `
name: two_skips
tasks:
  excluded:
    handler: shell
    condition: "false"
  after_condition:
    handler: shell
    depends_on: [excluded]
  routed:
    handler: shell
    on_result:
      success: skip
  after_route:
    handler: shell
    depends_on: [routed]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond)

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusSkipped, statuses["excluded"])
	assert.Equal(t, storage.TaskStatusCompleted, statuses["after_condition"],
		"a condition skip does NOT block: the dependent still runs")

	assert.Equal(t, storage.TaskStatusCompleted, statuses["routed"])
	assert.Equal(t, storage.TaskStatusSkipped, statuses["after_route"],
		"a route skip DOES block: the dependent does not run")

	assert.Equal(t, 2, worker.count(), "two tasks actually ran")
}
