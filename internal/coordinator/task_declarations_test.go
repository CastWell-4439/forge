package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// countWorker records how many times it was called, so a test can prove a task
// did NOT run.
type countWorker struct {
	forgev1.WorkerServiceClient
	mu     chan struct{}
	calls  int
	output string
}

func newCountWorker(output string) *countWorker {
	w := &countWorker{output: output}
	w.mu = make(chan struct{}, 1)
	w.mu <- struct{}{}
	return w
}

func (w *countWorker) ExecuteTask(_ context.Context, _ *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	<-w.mu
	w.calls++
	w.mu <- struct{}{}
	out := w.output
	if out == "" {
		out = `{"value":"ok"}`
	}
	return &forgev1.TaskResponse{Success: true, Output: json.RawMessage(out)}, nil
}

func (w *countWorker) count() int {
	<-w.mu
	defer func() { w.mu <- struct{}{} }()
	return w.calls
}

// taskConditionsTestSetup wires a coordinator with one worker per handler.
func taskConditionsTestSetup(t *testing.T, workers map[string]*countWorker) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	coord.mu.Lock()
	for name, w := range workers {
		coord.workers["w-"+name] = &WorkerEntry{
			ID:       "w-" + name,
			Handlers: []string{name},
			Capacity: 10,
			Client:   w,
		}
	}
	coord.mu.Unlock()
	return coord
}

// taskStatuses reads the stored status of each task, keyed by task name.
func taskStatuses(t *testing.T, store storage.Storage, workflowID string) map[string]storage.TaskStatus {
	t.Helper()
	tasks, err := store.ListTasksByWorkflow(context.Background(), workflowID)
	require.NoError(t, err)
	out := make(map[string]storage.TaskStatus, len(tasks))
	for _, task := range tasks {
		out[task.TaskName] = task.Status
	}
	return out
}

// --- conditions ---

// A task whose condition is false must not run, and must be recorded as
// SKIPPED rather than silently vanishing. Before this was wired, the condition
// was parsed into the DAG and then never consulted, so every task ran.
func TestFalseConditionSkipsTheTask(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_skip
tasks:
  a:
    handler: shell
    condition: "1 == 2"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return taskStatuses(t, coord.store, resp.GetWorkflowId())["a"] == storage.TaskStatusSkipped
	}, 10*time.Second, 50*time.Millisecond, "the task must end SKIPPED")

	assert.Zero(t, worker.count(), "a skipped task must never reach the worker")
}

// A true condition keeps the historical behaviour: the task runs.
func TestTrueConditionRunsTheTask(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_run
tasks:
  a:
    handler: shell
    condition: "1 == 1"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return taskStatuses(t, coord.store, resp.GetWorkflowId())["a"] == storage.TaskStatusCompleted
	}, 10*time.Second, 50*time.Millisecond)

	require.Eventually(t, func() bool { return worker.count() == 1 },
		20*time.Second, 25*time.Millisecond)
}

// No condition means "always run" — every workflow written before conditions
// existed must behave exactly as it did.
func TestAbsentConditionRunsTheTask(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: no_cond\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return taskStatuses(t, coord.store, resp.GetWorkflowId())["a"] == storage.TaskStatusCompleted
	}, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool { return worker.count() == 1 },
		20*time.Second, 25*time.Millisecond)
}

// A condition can read a predecessor's declared output, which is the whole
// point: the decision depends on what the run has learned so far.
func TestConditionReadsPredecessorOutput(t *testing.T) {
	// The first task reports confidence 0.5; the guarded task runs only when
	// confidence is high, so it must be skipped.
	worker := newCountWorker(`{"confidence":0.5}`)
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_upstream
tasks:
  plan:
    handler: shell
    output: plan
  approve:
    handler: shell
    depends_on: [plan]
    condition: "results.plan.confidence > 0.9"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["approve"] == storage.TaskStatusSkipped
	}, 10*time.Second, 50*time.Millisecond,
		"the guarded task must be skipped because plan reported low confidence")

	statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
	assert.Equal(t, storage.TaskStatusCompleted, statuses["plan"])
	// Only `plan` ran.
	assert.Equal(t, 1, worker.count(), "the guarded task must not reach the worker")
}

// The mirror image: the same shape with a confidence above the threshold runs
// both tasks. Without this, the test above could pass for the wrong reason
// (a condition context that never resolves anything would skip both).
func TestConditionRunsWhenPredecessorSatisfiesIt(t *testing.T) {
	worker := newCountWorker(`{"confidence":0.99}`)
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_upstream_run
tasks:
  plan:
    handler: shell
    output: plan
  approve:
    handler: shell
    depends_on: [plan]
    condition: "results.plan.confidence > 0.9"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["approve"] == storage.TaskStatusCompleted
	}, 10*time.Second, 50*time.Millisecond)

	require.Eventually(t, func() bool { return worker.count() == 2 },
		20*time.Second, 25*time.Millisecond, "both tasks must run")
}

// A skipped task satisfies its dependents, so the workflow finishes instead of
// stalling forever waiting on something that will never run.
func TestSkippedTaskUnlocksSuccessorsAndCompletesWorkflow(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_unlock
tasks:
  skipped:
    handler: shell
    condition: "false"
  after:
    handler: shell
    depends_on: [skipped]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["after"] == storage.TaskStatusCompleted
	}, 10*time.Second, 50*time.Millisecond, "the successor must run after the skip")

	wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		wf, err = coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 10*time.Second, 50*time.Millisecond,
		"a workflow whose only blocker was skipped must complete")

	assert.Equal(t, 1, worker.count(), "only the successor runs")
}

// A condition that cannot be evaluated is a configuration error, and it fails
// the task. Failing open would run work the author explicitly guarded.
func TestUnparseableConditionFailsTheTask(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: cond_bad
tasks:
  a:
    handler: shell
    condition: "this is not ( valid CEL"
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return taskStatuses(t, coord.store, resp.GetWorkflowId())["a"] == storage.TaskStatusFailed
	}, 10*time.Second, 50*time.Millisecond,
		"an unevaluable condition must fail the task, not run it")

	assert.Zero(t, worker.count(), "the guarded task must not run on a broken condition")
}

// --- deadlines and ownership ---

// Submitting must record each task's deadline, because the timeout scanner
// finds work by that field — without it, a hung task stayed RUNNING forever.
func TestSubmitRecordsTaskDeadline(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	dagYAML := `
name: deadline
timeout: 30m
tasks:
  a:
    handler: shell
    timeout: 5m
  b:
    handler: shell
    depends_on: [a]
`
	before := time.Now()
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.Len(t, tasks, 2)

	for _, task := range tasks {
		require.NotNil(t, task.TimeoutAt, "task %s must carry a deadline", task.TaskName)

		want := 30 * time.Minute
		if task.TaskName == "a" {
			want = 5 * time.Minute // the task's own timeout wins over the workflow's
		}
		got := task.TimeoutAt.Sub(before)
		assert.InDelta(t, want.Seconds(), got.Seconds(), 60,
			"task %s deadline should be ~%s after submit", task.TaskName, want)
	}
}

// A workflow that declares no timeout leaves deadlines unset, which the
// scanner already reads as "no deadline" rather than "expired at zero".
func TestNoDeclaredTimeoutLeavesDeadlineUnset(t *testing.T) {
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: no_deadline\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Nil(t, tasks[0].TimeoutAt, "no declaration means no deadline")
}

// Dispatch records which worker owns the task. The dead-worker path matches on
// exactly this field, so without the write a task whose worker died could never
// be found and requeued.
func TestDispatchRecordsTaskOwner(t *testing.T) {
	var seen []string
	worker := newCountWorker("")
	coord := taskConditionsTestSetup(t, map[string]*countWorker{"shell": worker})

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: owner\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
		if err != nil || len(tasks) == 0 {
			return false
		}
		seen = append(seen, tasks[0].WorkerID)
		return tasks[0].WorkerID != ""
	}, 10*time.Second, 50*time.Millisecond, "the dispatched task must record its worker")

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	assert.Equal(t, "w-shell", tasks[0].WorkerID)
}

// --- helper behaviour ---

func TestTaskDeadlinePrefersTaskThenWorkflow(t *testing.T) {
	now := time.Now()

	got := taskDeadline(5*time.Minute, 30*time.Minute, now)
	require.NotNil(t, got)
	assert.Equal(t, now.Add(5*time.Minute), *got, "the task's own timeout wins")

	got = taskDeadline(0, 30*time.Minute, now)
	require.NotNil(t, got)
	assert.Equal(t, now.Add(30*time.Minute), *got, "the workflow timeout is the fallback")

	assert.Nil(t, taskDeadline(0, 0, now), "no declaration means no deadline")
	assert.Nil(t, taskDeadline(-time.Minute, 0, now), "a negative timeout is no timeout")
}
