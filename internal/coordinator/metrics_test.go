package coordinator

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// recordingSink captures what the coordinator reported, so a test can assert on
// the observations rather than on the metrics backend.
type recordingSink struct {
	mu       sync.Mutex
	finished []string
	tasks    []struct{ handler, status string }
	retries  []struct{ handler, reason string }
	active   []float64
	queue    []float64
}

func (s *recordingSink) WorkflowFinished(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, status)
}

func (s *recordingSink) TaskFinished(handler, status string, _ float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, struct{ handler, status string }{handler, status})
}

func (s *recordingSink) TaskRetried(handler, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries = append(s.retries, struct{ handler, reason string }{handler, reason})
}

func (s *recordingSink) ActiveWorkflows(n float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = append(s.active, n)
}

func (s *recordingSink) QueueDepth(n float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, n)
}

func (s *recordingSink) snapshot() (finished []string, tasks int, queue []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.finished...), len(s.tasks), append([]float64(nil), s.queue...)
}

// A workflow reaching a terminal state must be counted. Before this was wired
// the endpoint published the series and nothing ever wrote to it, so it read
// zero for every deployment — indistinguishable from a system doing no work.
func TestFinishedWorkflowIsCounted(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	sink := &recordingSink{}
	coord.SetMetrics(sink)

	worker := newCountWorker("")
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: metric_ok\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		finished, _, _ := sink.snapshot()
		return len(finished) == 1
	}, 15*time.Second, 25*time.Millisecond, "completing a workflow must be counted")

	finished, tasks, _ := sink.snapshot()
	assert.Equal(t, []string{"COMPLETED"}, finished)
	assert.GreaterOrEqual(t, tasks, 1, "the task's duration must be observed too")
}

// A failed workflow is counted under its own status, not as a completion.
func TestFailedWorkflowIsCounted(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "metrics2.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	sink := &recordingSink{}
	coord.SetMetrics(sink)

	// A worker that always fails, with no retry policy: the task fails on its
	// first attempt and the workflow follows.
	worker := &flakyWorker{failFirst: 99, errMsg: "permanent"}
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: metric_fail\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		finished, _, _ := sink.snapshot()
		return len(finished) == 1
	}, 15*time.Second, 25*time.Millisecond, "a failed workflow must be counted")

	finished, _, _ := sink.snapshot()
	assert.Equal(t, []string{"FAILED"}, finished)
}

// A retry is counted as a retry, not as a failure: the task has not given up.
func TestRetryIsCountedAsARetry(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "metrics3.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	sink := &recordingSink{}
	coord.SetMetrics(sink)

	worker := &flakyWorker{failFirst: 1, errMsg: "transient"}
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: metric_retry\ntasks:\n  a:\n    handler: shell\n    retry:\n      max_attempts: 2\n      backoff: fixed\n      initial_interval: 10ms\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.retries) == 1
	}, 20*time.Second, 25*time.Millisecond, "a rescheduled task must be counted")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Equal(t, "shell", sink.retries[0].handler)
	assert.NotEmpty(t, sink.retries[0].reason, "a retry count without a reason is not actionable")
}

// Queue depth is reported as tasks wait, so the series reflects something.
func TestQueueDepthIsReported(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "metrics4.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	sink := &recordingSink{}
	coord.SetMetrics(sink)

	// No worker for this handler, so the task stays READY and is counted as
	// waiting.
	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: metric_queue\ntasks:\n  a:\n    handler: nobody\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, _, queue := sink.snapshot()
		return len(queue) > 0
	}, 15*time.Second, 25*time.Millisecond, "a waiting task must be reported as queued")
}

// Without a sink nothing is reported and nothing panics: the coordinator works
// exactly as it did before metrics were wired, which is why the sink is
// optional.
func TestNoSinkIsAcceptable(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "metrics5.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store) // no SetMetrics
	worker := newCountWorker("")
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: worker}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: metric_none\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), resp.GetWorkflowId())
		return err == nil && wf.Status == storage.WorkflowStatusCompleted
	}, 15*time.Second, 25*time.Millisecond)
}
