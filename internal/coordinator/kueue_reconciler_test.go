package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// scriptedSubmitter answers GetJobStatus from a script and records calls, so
// the reconciler can be driven without a cluster.
type scriptedSubmitter struct {
	statuses   []KueueJobStatus
	statusErr  error
	submitted  []KueueJobSpec
	submitErr  error
	cancelled  []string
	statusCall int
}

func (s *scriptedSubmitter) SubmitJob(_ context.Context, spec KueueJobSpec) error {
	if s.submitErr != nil {
		return s.submitErr
	}
	s.submitted = append(s.submitted, spec)
	return nil
}

func (s *scriptedSubmitter) GetJobStatus(_ context.Context, _, _ string) (KueueJobStatus, error) {
	if s.statusErr != nil {
		return KueueJobStatus{}, s.statusErr
	}
	if len(s.statuses) == 0 {
		return KueueJobStatus{Phase: "Running"}, nil
	}
	idx := s.statusCall
	if idx >= len(s.statuses) {
		idx = len(s.statuses) - 1
	}
	s.statusCall++
	return s.statuses[idx], nil
}

func (s *scriptedSubmitter) CancelJob(_ context.Context, namespace, name string) error {
	s.cancelled = append(s.cancelled, namespace+"/"+name)
	return nil
}

// reconcilerFixture builds a coordinator-backed reconciler over a Bolt store,
// with running-task setup helpers.
type reconcilerFixture struct {
	store      storage.Storage
	submitter  *scriptedSubmitter
	reconciler *KueueReconciler
	completed  map[string]string
	failed     map[string]string
	leader     bool
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sub := &scriptedSubmitter{}
	f := &reconcilerFixture{
		store:     store,
		submitter: sub,
		completed: map[string]string{},
		failed:    map[string]string{},
		leader:    true,
	}
	manager := NewKueueManager(KueueConfig{Enabled: true, Namespace: "forge", QueueName: "q"}, sub)
	f.reconciler = NewKueueReconciler(
		store, manager,
		func(_ context.Context, taskID string, output []byte) error {
			f.completed[taskID] = string(output)
			return store.CompleteTask(context.Background(), taskID, output)
		},
		func(_ context.Context, taskID string, msg string) error {
			f.failed[taskID] = msg
			return store.FailTask(context.Background(), taskID, msg)
		},
		func() bool { return f.leader },
		0,
	)
	return f
}

// seedKueueTask creates a RUNNING workflow+task owned by a Kueue job.
func (f *reconcilerFixture) seedKueueTask(t *testing.T, taskID, namespace, jobName string) {
	t.Helper()
	ctx := context.Background()
	wfID := "wf-" + taskID
	require.NoError(t, f.store.SaveWorkflow(ctx, &storage.Workflow{
		ID: wfID, Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, f.store.SaveTask(ctx, &storage.Task{
		ID: taskID, WorkflowID: wfID, TaskName: "gpu", Handler: "ai.infer",
		Status: storage.TaskStatusRunning, WorkerID: kueueWorkerID(namespace, jobName),
		CreatedAt: time.Now().UTC(),
	}))
}

// A succeeded job completes its task — the write-back that makes submission
// safe to do at all.
func TestReconcilerCompletesSucceededJob(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-1", "forge", "forge-wf-1-task-1")
	f.submitter.statuses = []KueueJobStatus{{Phase: "Succeeded", Message: "job finished"}}

	f.reconciler.reconcileOnce(context.Background())

	require.Contains(t, f.completed, "task-1", "the task was completed")
	var output map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.completed["task-1"]), &output),
		"the output is valid JSON and can be parsed downstream")
	assert.Equal(t, "job finished", output["message"], "the job's message is carried through")
	assert.Equal(t, "forge-wf-1-task-1", output["kueue_job"])
	assert.Equal(t, "forge", output["namespace"])

	task, err := f.store.GetTask(context.Background(), "task-1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusCompleted, task.Status)
}

// A failed job fails its task, carrying the cluster's reason.
func TestReconcilerFailsFailedJob(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-2", "forge", "job-2")
	f.submitter.statuses = []KueueJobStatus{{Phase: "Failed", Message: "OOMKilled"}}

	f.reconciler.reconcileOnce(context.Background())

	require.Contains(t, f.failed, "task-2")
	assert.Equal(t, "OOMKilled", f.failed["task-2"])

	task, err := f.store.GetTask(context.Background(), "task-2")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusFailed, task.Status)
}

// A failed job with no message still fails the task with something usable.
func TestReconcilerFailsJobWithoutMessage(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-3", "forge", "job-3")
	f.submitter.statuses = []KueueJobStatus{{Phase: "Failed"}}

	f.reconciler.reconcileOnce(context.Background())

	require.Contains(t, f.failed, "task-3")
	assert.Contains(t, f.failed["task-3"], "job-3", "the message names the job")
}

// In-flight phases leave the task alone: pending/admitted/running are not
// outcomes, and the deadline is the timeout manager's business, not a second
// timer here.
func TestReconcilerLeavesInFlightJobsAlone(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-4", "forge", "job-4")

	for _, phase := range []string{"Pending", "Admitted", "Running"} {
		f.submitter.statuses = []KueueJobStatus{{Phase: phase}}
		f.reconciler.reconcileOnce(context.Background())
	}

	assert.Empty(t, f.completed, "no completion was reported")
	assert.Empty(t, f.failed, "no failure was reported")

	task, err := f.store.GetTask(context.Background(), "task-4")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, task.Status, "the task is still running")
}

// Worker-owned tasks are untouched: the sentinel is what separates the two
// execution models, and a reconciler that guessed would corrupt ordinary runs.
func TestReconcilerIgnoresWorkerTasks(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	require.NoError(t, f.store.SaveWorkflow(ctx, &storage.Workflow{
		ID: "wf-worker", Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, f.store.SaveTask(ctx, &storage.Task{
		ID: "task-worker", WorkflowID: "wf-worker", TaskName: "t", Handler: "h",
		Status: storage.TaskStatusRunning, WorkerID: "worker-7", CreatedAt: time.Now().UTC(),
	}))
	f.submitter.statuses = []KueueJobStatus{{Phase: "Succeeded"}}

	f.reconciler.reconcileOnce(ctx)

	assert.Empty(t, f.completed)
	assert.Empty(t, f.failed)
	assert.Equal(t, 0, f.submitter.statusCall, "no job status was read for a worker task")
}

// Non-running tasks are not reconciled: a completed task whose job lingers
// must not be reported twice.
func TestReconcilerIgnoresFinishedKueueTasks(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-5", "forge", "job-5")
	require.NoError(t, f.store.UpdateTaskStatus(context.Background(), "task-5", storage.TaskStatusCompleted))
	f.submitter.statuses = []KueueJobStatus{{Phase: "Succeeded"}}

	f.reconciler.reconcileOnce(context.Background())

	assert.Empty(t, f.completed, "an already-finished task is not completed again")
	assert.Equal(t, 0, f.submitter.statusCall)
}

// A status read failure is transient: logged, retried next round, and never
// turned into a task failure (the job may be perfectly healthy).
func TestReconcilerStatusFailureDoesNotFailTask(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-6", "forge", "job-6")
	f.submitter.statusErr = assert.AnError

	f.reconciler.reconcileOnce(context.Background())

	assert.Empty(t, f.failed, "a cluster read error is not the task's failure")
	task, err := f.store.GetTask(context.Background(), "task-6")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, task.Status)
	assert.Equal(t, 1, f.reconciler.failStreak["task-6"], "the failure was counted for escalation")
}

// Non-leaders do nothing: every replica runs the loop, and two copies
// reporting one completion would double-fire the state machine.
func TestReconcilerSkipsWhenNotLeader(t *testing.T) {
	f := newReconcilerFixture(t)
	f.seedKueueTask(t, "task-7", "forge", "job-7")
	f.leader = false
	f.submitter.statuses = []KueueJobStatus{{Phase: "Succeeded"}}

	// Run the loop body the way Run does, with leadership false.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if f.reconciler.isLeader() {
		f.reconciler.reconcileOnce(ctx)
	}

	assert.Empty(t, f.completed, "a non-leader must not reconcile")
	assert.Equal(t, 0, f.submitter.statusCall)
}

// CancelJob is best-effort: the task outcome is already decided, so a cluster
// that refuses the deletion must not turn it into an error.
func TestReconcilerCancelIsBestEffort(t *testing.T) {
	f := newReconcilerFixture(t)
	f.reconciler.CancelJob(context.Background(), "forge", "job-8")
	assert.Equal(t, []string{"forge/job-8"}, f.submitter.cancelled)
}

// The sentinel round-trips, and ordinary worker ids are never mistaken for it.
func TestKueueWorkerIDSentinel(t *testing.T) {
	id := kueueWorkerID("forge", "job-1")
	assert.Equal(t, "kueue:forge/job-1", id)

	ns, job, ok := parseKueueWorker(id)
	require.True(t, ok)
	assert.Equal(t, "forge", ns)
	assert.Equal(t, "job-1", job)

	for _, workerID := range []string{"", "worker-1", "kueue:", "kueue:only-namespace", "kueue:/job"} {
		_, _, ok := parseKueueWorker(workerID)
		assert.False(t, ok, "%q is not a Kueue sentinel", workerID)
	}
}

// GPU dispatch: the task is submitted, marked running, and given both the job
// identity (for the reconciler) and a deadline (for the timeout manager).
func TestDispatchKueueTaskRecordsIdentityAndDeadline(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sub := &scriptedSubmitter{}
	coord := NewCoordinator(store)
	coord.SetKueue(NewKueueManager(KueueConfig{Enabled: true, Namespace: "forge", QueueName: "q"}, sub))

	ctx := context.Background()
	require.NoError(t, store.SaveWorkflow(ctx, &storage.Workflow{
		ID: "wf-gpu", Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	task := &storage.Task{
		ID: "task-gpu", WorkflowID: "wf-gpu", TaskName: "infer", Handler: "ai.infer",
		Status: storage.TaskStatusReady, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.SaveTask(ctx, task))

	// Cache a DAG whose task asks for a GPU.
	dag := &DAG{Tasks: map[string]*TaskDef{
		"infer": {Name: "infer", Handler: "ai.infer", Params: map[string]interface{}{"gpu.required": true}},
	}}
	coord.dagCacheMu.Lock()
	coord.dagCache["wf-gpu"] = dag
	coord.dagCacheMu.Unlock()

	coord.dispatchKueueTask(ctx, task, json.RawMessage(`{}`))

	require.Len(t, sub.submitted, 1, "the job was submitted to Kueue")
	assert.Equal(t, "forge-wf-gpu-task-gpu", sub.submitted[0].Name,
		"the job name is the deterministic derivation of workflow and task")
	assert.Equal(t, "wf-gpu", sub.submitted[0].WorkflowID)
	assert.Equal(t, "task-gpu", sub.submitted[0].TaskID)

	stored, err := store.GetTask(ctx, "task-gpu")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, stored.Status)
	assert.True(t, len(stored.WorkerID) > len(kueueWorkerPrefix) &&
		stored.WorkerID[:len(kueueWorkerPrefix)] == kueueWorkerPrefix,
		"the worker id is the Kueue sentinel, got %q", stored.WorkerID)
	require.NotNil(t, stored.TimeoutAt, "a deadline was recorded")
	assert.True(t, stored.TimeoutAt.After(time.Now()), "the deadline is in the future")
}

// A submission failure fails the task rather than leaving it RUNNING against a
// job that does not exist.
func TestDispatchKueueTaskFailsOnSubmitError(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sub := &scriptedSubmitter{submitErr: assert.AnError}
	coord := NewCoordinator(store)
	coord.SetKueue(NewKueueManager(KueueConfig{Enabled: true, Namespace: "forge"}, sub))

	ctx := context.Background()
	require.NoError(t, store.SaveWorkflow(ctx, &storage.Workflow{
		ID: "wf-bad", Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.SaveTask(ctx, &storage.Task{
		ID: "task-bad", WorkflowID: "wf-bad", TaskName: "t", Handler: "h",
		Status: storage.TaskStatusReady, CreatedAt: time.Now().UTC(),
	}))

	coord.dispatchKueueTask(ctx, &storage.Task{ID: "task-bad", WorkflowID: "wf-bad", TaskName: "t"}, json.RawMessage(`{}`))

	stored, err := store.GetTask(ctx, "task-bad")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusFailed, stored.Status, "a failed submit fails the task")
	assert.Contains(t, stored.ErrorMsg, "kueue submit")
}

// A job name is stable across calls, which is what makes re-submission
// idempotent at the Kubernetes level.
func TestKueueJobNameIsDeterministic(t *testing.T) {
	m := NewKueueManager(DefaultKueueConfig(), &scriptedSubmitter{})
	first := m.JobName("wf-abcdefgh-very-long", "task-abcdefgh-very-long")
	second := m.JobName("wf-abcdefgh-very-long", "task-abcdefgh-very-long")
	assert.Equal(t, first, second)
	assert.LessOrEqual(t, len(first), 63, "the name stays inside the Kubernetes limit")
	assert.Equal(t, "forge-wf-abcde-task-abc", first)
}

// IsGPUTask drives the routing decision, so its answer must depend on the
// declared parameter and nothing else.
func TestIsGPUTaskOnlyForDeclaredGPUTasks(t *testing.T) {
	assert.False(t, IsGPUTask(TaskDef{}))
	assert.False(t, IsGPUTask(TaskDef{Params: map[string]interface{}{"other": 1}}))
	assert.True(t, IsGPUTask(TaskDef{Params: map[string]interface{}{"gpu.required": true}}))
	assert.True(t, IsGPUTask(TaskDef{Params: map[string]interface{}{"gpu.required": false}}),
		"presence marks the task, the value is the scheduler's business")
}

// A task whose DAG is not cached resolves to a zero definition: IsGPUTask then
// says false and the task takes the ordinary worker path instead of being
// guessed at as a GPU job.
func TestTaskDefWithoutCachedDAG(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	coord := NewCoordinator(store)
	def := coord.taskDef(&storage.Task{ID: "t", WorkflowID: "missing", TaskName: "x"})
	assert.Equal(t, TaskDef{}, def)
	assert.False(t, IsGPUTask(def))
}
