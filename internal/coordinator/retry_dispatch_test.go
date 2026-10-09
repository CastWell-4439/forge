package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// flakyWorker fails a configurable number of times before succeeding, so a test
// can drive the retry path deterministically.
type flakyWorker struct {
	forgev1.WorkerServiceClient
	mu        sync.Mutex
	calls     int
	failFirst int
	errMsg    string
}

func (w *flakyWorker) ExecuteTask(_ context.Context, _ *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls <= w.failFirst {
		return &forgev1.TaskResponse{Success: false, ErrorMsg: w.errMsg}, nil
	}
	return &forgev1.TaskResponse{Success: true, Output: json.RawMessage(`{"ok":true}`)}, nil
}

func (w *flakyWorker) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// retryTestSetup wires one worker for the shell handler.
func retryTestSetup(t *testing.T, worker *flakyWorker) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "retry.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{
		ID:       "w-shell",
		Handlers: []string{"shell"},
		Capacity: 10,
		Client:   worker,
	}
	coord.mu.Unlock()
	return coord
}

// A task with attempts left must be retried, not failed. Before this was wired,
// max_attempts was parsed and copied onto the task row and then never consulted,
// so the first failure always failed the workflow.
func TestTaskRetriesUntilItSucceeds(t *testing.T) {
	worker := &flakyWorker{failFirst: 2, errMsg: "transient"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: retry_ok
tasks:
  a:
    handler: shell
    retry:
      max_attempts: 3
      backoff: fixed
      initial_interval: 10ms
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["a"] == storage.TaskStatusCompleted
	}, 20*time.Second, 25*time.Millisecond, "the third attempt must succeed")

	// The task reaching COMPLETED and the workflow reaching COMPLETED are two
	// writes; waiting only for the first leaves a window where the second has
	// not landed yet.
	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 20*time.Second, 25*time.Millisecond, "the workflow must complete once its only task has")

	// max_attempts is the total number of runs: 3 attempts means the first two
	// may fail and the third succeeds.
	assert.Equal(t, 3, worker.callCount(), "two failures then one success")

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, 3, tasks[0].Attempt, "the attempt counter counts starts")
}

// Exhausting the attempts fails the task, and the workflow with it.
func TestRetriesExhaustedFailsTheTask(t *testing.T) {
	worker := &flakyWorker{failFirst: 99, errMsg: "permanent"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: retry_exhausted
tasks:
  a:
    handler: shell
    retry:
      max_attempts: 2
      backoff: fixed
      initial_interval: 10ms
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond, "the workflow must fail once attempts are spent")

	assert.Equal(t, 2, worker.callCount(), "exactly max_attempts calls")

	tasks, err := coord.store.ListTasksByWorkflow(context.Background(), resp.GetWorkflowId())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, storage.TaskStatusFailed, tasks[0].Status)
	assert.Equal(t, 2, tasks[0].Attempt, "the attempt counter must have advanced")
}

// A task that declares no retry policy fails on the first failure, exactly as
// it did before retries existed. This is the regression guard for the whole
// feature: every workflow written earlier has no retry block.
func TestNoRetryPolicyFailsImmediately(t *testing.T) {
	worker := &flakyWorker{failFirst: 99, errMsg: "boom"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: no_retry
tasks:
  a:
    handler: shell
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond)

	assert.Equal(t, 1, worker.callCount(), "no policy means one attempt")
}

// max_attempts: 1 is the same as declaring nothing: one try.
func TestSingleAttemptPolicyDoesNotRetry(t *testing.T) {
	worker := &flakyWorker{failFirst: 99, errMsg: "boom"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: one_attempt
tasks:
  a:
    handler: shell
    retry:
      max_attempts: 1
      initial_interval: 10ms
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond)

	assert.Equal(t, 1, worker.callCount())
}

// The backoff is a real delay: a task that is READY with a future ScheduledAt
// must not be dispatched early. Without the gate, "retry after 2s" would run
// immediately and the interval would be decorative.
func TestRetryWaitsOutItsBackoff(t *testing.T) {
	worker := &flakyWorker{failFirst: 1, errMsg: "transient"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: backoff_wait
tasks:
  a:
    handler: shell
    retry:
      max_attempts: 2
      backoff: fixed
      initial_interval: 700ms
`
	start := time.Now()
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		statuses := taskStatuses(t, coord.store, resp.GetWorkflowId())
		return statuses["a"] == storage.TaskStatusCompleted
	}, 20*time.Second, 25*time.Millisecond)

	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 600*time.Millisecond,
		"the second attempt must wait for the backoff, not run immediately")
	assert.Equal(t, 2, worker.callCount())
}

// A retry records itself in the event stream, so an operator can tell "it is
// being retried" from "it failed".
func TestRetryRecordsAnEvent(t *testing.T) {
	worker := &flakyWorker{failFirst: 1, errMsg: "transient"}
	coord := retryTestSetup(t, worker)

	dagYAML := `
name: retry_event
tasks:
  a:
    handler: shell
    retry:
      max_attempts: 2
      backoff: fixed
      initial_interval: 10ms
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		events, err := coord.store.GetWorkflowHistory(context.Background(), resp.GetWorkflowId())
		if err != nil {
			return false
		}
		for _, e := range events {
			if e.Type == storage.EventTaskRetrying {
				return true
			}
		}
		return false
	}, 20*time.Second, 25*time.Millisecond, "a retry must appear in the event log")
}

// --- helper ---

func TestEvaluateRetryRespectsTheBudget(t *testing.T) {
	policy := RetryPolicy{BackoffType: BackoffFixed, InitialInterval: 50 * time.Millisecond}

	// Attempt 0 of 3: retry with the declared delay.
	d := EvaluateRetry(&RetryableTask{Attempt: 0, MaxAttempts: 3, RetryPolicy: policy})
	require.True(t, d.ShouldRetry)
	assert.Equal(t, 1, d.NextAttempt)
	assert.Equal(t, 50*time.Millisecond, d.Delay, "fixed backoff ignores the attempt number")

	// Attempt 3 of 3: budget spent.
	d = EvaluateRetry(&RetryableTask{Attempt: 3, MaxAttempts: 3, RetryPolicy: policy})
	assert.False(t, d.ShouldRetry)
}

// Exponential backoff with jitter lands in [0, base*multiplier^(n-1)); the test
// bounds it rather than pinning a value, because jitter is the point.
func TestCalculateBackoffStaysWithinBounds(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffExponential,
		InitialInterval: 100 * time.Millisecond,
		Multiplier:      2,
		MaxInterval:     10 * time.Second,
	}

	for attempt := 1; attempt <= 5; attempt++ {
		delay := calculateBackoff(attempt, policy)
		ceiling := time.Duration(float64(policy.InitialInterval) * pow(2, attempt-1))
		assert.GreaterOrEqual(t, delay, time.Duration(0))
		assert.LessOrEqual(t, delay, ceiling, "attempt %d must stay under the exponential ceiling", attempt)
	}

	// The cap applies however large the exponent grows.
	delay := calculateBackoff(20, policy)
	assert.LessOrEqual(t, delay, policy.MaxInterval)
}

func pow(base float64, exp int) float64 {
	out := 1.0
	for i := 0; i < exp; i++ {
		out *= base
	}
	return out
}
