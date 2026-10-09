package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// on_failure: CONTINUE means the failed task does not stop the run: its
// dependents are released and the workflow proceeds. The task itself stays
// FAILED — the failure is real and the log says so.
func TestOnFailureContinueReleasesDependents(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "cont.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)

	// `a` always fails and declares CONTINUE; `b` depends on it and succeeds.
	failing := &flakyWorker{failFirst: 99, errMsg: "expected failure"}
	succeeding := newCountWorker("")
	coord.mu.Lock()
	coord.workers["w-fail"] = &WorkerEntry{ID: "w-fail", Handlers: []string{"fail"}, Capacity: 10, Client: failing}
	coord.workers["w-ok"] = &WorkerEntry{ID: "w-ok", Handlers: []string{"ok"}, Capacity: 10, Client: succeeding}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: `
name: continue_chain
tasks:
  a:
    handler: fail
    on_failure: CONTINUE
  b:
    handler: ok
    depends_on: [a]
`,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["b"] == storage.TaskStatusCompleted
	}, 20*time.Second, 25*time.Millisecond,
		"the dependent must run even though its predecessor failed")

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusFailed, statuses["a"],
		"the failure is still recorded: CONTINUE does not make the task succeed")
	assert.Equal(t, 1, succeeding.count())

	wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	assert.NotEqual(t, storage.WorkflowStatusFailed, wf.Status,
		"a run whose optional branch failed is not a failed run")
}

// The default (no on_failure, or FAIL_WORKFLOW) still fails the whole run: the
// new branch must not change the behaviour everyone already relies on.
func TestOnFailureDefaultStillFailsTheWorkflow(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "def.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	failing := &flakyWorker{failFirst: 99, errMsg: "boom"}
	succeeding := newCountWorker("")
	coord.mu.Lock()
	coord.workers["w-fail"] = &WorkerEntry{ID: "w-fail", Handlers: []string{"fail"}, Capacity: 10, Client: failing}
	coord.workers["w-ok"] = &WorkerEntry{ID: "w-ok", Handlers: []string{"ok"}, Capacity: 10, Client: succeeding}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: `
name: default_chain
tasks:
  a:
    handler: fail
  b:
    handler: ok
    depends_on: [a]
`,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond, "an undeclared policy fails the run")

	assert.Zero(t, succeeding.count(), "and the dependent does not run")
}

// FAIL_WORKFLOW named explicitly behaves exactly like the default, so the two
// spellings cannot drift apart.
func TestOnFailureFailWorkflowMatchesDefault(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "explicit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	failing := &flakyWorker{failFirst: 99, errMsg: "boom"}
	coord.mu.Lock()
	coord.workers["w-fail"] = &WorkerEntry{ID: "w-fail", Handlers: []string{"fail"}, Capacity: 10, Client: failing}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: explicit_fail\ntasks:\n  a:\n    handler: fail\n    on_failure: FAIL_WORKFLOW\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond)
}

// --- deadlines on the wire ---

// The dispatched request carries the remaining time, so a worker can bound its
// own work instead of being killed from outside with no chance to clean up.
func TestDispatchedRequestCarriesRemainingTimeout(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "to.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	worker := &timeoutCapturingWorker{ch: make(chan int64, 4)}
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: to_set\ntasks:\n  a:\n    handler: shell\n    timeout: 5m\n",
	})
	require.NoError(t, err)

	select {
	case ms := <-worker.ch:
		// Close to 5 minutes, allowing for the time spent getting here.
		assert.Greater(t, ms, int64(4*60*1000), "the remaining window is sent")
		assert.LessOrEqual(t, ms, int64(5*60*1000), "and is not longer than declared")
	case <-time.After(15 * time.Second):
		t.Fatal("task was never dispatched")
	}
}

// A task with no declared deadline sends 0, which is what "no deadline" has
// always meant here.
func TestDispatchedRequestSendsZeroWithoutDeadline(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "to2.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	worker := &timeoutCapturingWorker{ch: make(chan int64, 4)}
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: to_none\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	select {
	case ms := <-worker.ch:
		assert.Zero(t, ms, "no declared deadline means no timeout on the wire")
	case <-time.After(15 * time.Second):
		t.Fatal("task was never dispatched")
	}
}

func TestRemainingTimeoutMs(t *testing.T) {
	now := time.Now()

	assert.Zero(t, remainingTimeoutMs(nil, now), "no deadline means zero")

	future := now.Add(90 * time.Second)
	assert.InDelta(t, 90000, remainingTimeoutMs(&future, now), 1000)

	// An already-passed deadline reports zero rather than a negative number: the
	// sweep is about to fail the task, and a negative timeout is not something a
	// worker can act on.
	past := now.Add(-time.Minute)
	assert.Zero(t, remainingTimeoutMs(&past, now))
}

// timeoutCapturingWorker records the timeout_ms it was sent.
type timeoutCapturingWorker struct {
	forgev1.WorkerServiceClient
	ch chan int64
}

func (w *timeoutCapturingWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	w.ch <- req.GetTimeoutMs()
	return &forgev1.TaskResponse{Success: true, Output: []byte(`{}`)}, nil
}
