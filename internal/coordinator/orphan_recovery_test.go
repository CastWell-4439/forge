package coordinator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// newReclaimCoordinator builds a coordinator with an empty worker registry,
// which is exactly the state after a restart.
func newReclaimCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "reclaim.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return NewCoordinator(store)
}

// seedRunningTask writes a workflow in RUNNING with one task in the given state,
// assigned to workerID.
func seedRunningTask(t *testing.T, c *Coordinator, workflowID, taskID, workerID string, status storage.TaskStatus) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, c.store.SaveWorkflow(ctx, &storage.Workflow{
		ID: workflowID, Name: "reclaim-test", Status: storage.WorkflowStatusRunning,
	}))
	require.NoError(t, c.store.SaveTask(ctx, &storage.Task{
		ID: taskID, WorkflowID: workflowID, TaskName: taskID,
		Handler: "shell", Status: status, WorkerID: workerID,
		MaxAttempts: 3, Attempt: 1,
	}))
}

// THE DEFECT, as a test.
//
// A coordinator that restarts has an empty worker registry, but the task rows
// still say RUNNING. Nothing used to look for them, so the workflow sat in
// RUNNING forever with no worker and no way forward — a state indistinguishable
// from healthy progress.
//
// This test is the guard: without the reclaim pass it fails, because the task
// stays RUNNING.
func TestRestartReclaimsTasksItCannotAccountFor(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-restart", "t1", "worker-that-is-gone", storage.TaskStatusRunning)

	// The registry is empty: this process just started and knows no workers.
	coord.ReclaimOrphanedTasks(ctx)

	task, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, task.Status,
		"a task whose worker this coordinator cannot name must be recovered, not left RUNNING forever")
}

// A task whose worker IS known must not be touched.
//
// This is the conservative half, and the more important one: resetting a task
// that another process is actively running would re-execute it, and a task with
// a side effect cannot be un-executed.
func TestKnownWorkerTaskIsNotReclaimed(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-live", "t1", "worker-1", storage.TaskStatusRunning)

	// Register the worker, so this coordinator can account for it.
	require.NoError(t, coord.RegisterWorker(ctx, "worker-1", "127.0.0.1:1", []string{"shell"}, 4))

	coord.ReclaimOrphanedTasks(ctx)

	task, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, task.Status,
		"a task running on a worker this coordinator knows must be left alone")
}

// A task belonging to a worker this coordinator has never heard of is reclaimed
// even when other workers are registered.
//
// The registry being non-empty is not evidence that a PARTICULAR worker is
// alive, and treating it as such would strand every task whose worker died while
// others lived — the common case in a fleet.
func TestTaskOfAnUnknownWorkerIsReclaimedAlongsideKnownOnes(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-mixed", "known", "worker-1", storage.TaskStatusRunning)
	seedRunningTask(t, coord, "wf-mixed", "orphan", "worker-vanished", storage.TaskStatusRunning)

	require.NoError(t, coord.RegisterWorker(ctx, "worker-1", "127.0.0.1:1", []string{"shell"}, 4))

	coord.ReclaimOrphanedTasks(ctx)

	known, err := coord.store.GetTask(ctx, "known")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, known.Status, "the live worker's task stays")

	orphan, err := coord.store.GetTask(ctx, "orphan")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, orphan.Status, "the vanished worker's task is recovered")
}

// SCHEDULED counts too: a task assigned to a worker that never got to run it is
// just as stuck as a RUNNING one, and it is in that state precisely because the
// process that assigned it is gone.
func TestScheduledTasksAreAlsoReclaimed(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-sched", "t1", "worker-gone", storage.TaskStatusScheduled)

	coord.ReclaimOrphanedTasks(ctx)

	task, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, task.Status)
}

// A task with NO worker recorded is reclaimed: nobody was ever assigned, so it
// cannot be running anywhere, and leaving it stuck helps no one.
func TestTaskWithNoWorkerIsReclaimed(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-noworker", "t1", "", storage.TaskStatusRunning)

	coord.ReclaimOrphanedTasks(ctx)

	task, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, task.Status)
}

// A settled task is never touched, whatever its worker says.
//
// Reclaim is about work in flight. A COMPLETED task whose worker has since died
// is finished business, and resetting it would re-run a task that already
// happened.
//
// READY is deliberately not in this list: it is the state reclaim moves tasks
// TO, so asserting "a READY task is not READY" would be a contradiction rather
// than a check.
func TestSettledTasksAreNeverReclaimed(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	statuses := []storage.TaskStatus{
		storage.TaskStatusCompleted,
		storage.TaskStatusFailed,
		storage.TaskStatusSkipped,
		storage.TaskStatusCompensating,
	}
	for i, status := range statuses {
		taskID := "settled-" + string(rune('a'+i))
		seedRunningTask(t, coord, "wf-settled", taskID, "worker-gone", status)
	}

	coord.ReclaimOrphanedTasks(ctx)

	for i, status := range statuses {
		taskID := "settled-" + string(rune('a'+i))
		task, err := coord.store.GetTask(ctx, taskID)
		require.NoError(t, err)
		assert.Equal(t, status, task.Status,
			"%s was settled as %s and must not be resurrected", taskID, status)
	}
}

// Reclaiming does NOT consume a retry attempt.
//
// The two are different events. `Attempt` counts executions under the retry
// policy; a restart is not the task's fault and must not eat its budget — a
// deployment restarted three times would otherwise exhaust `max_attempts`
// without the task ever failing.
func TestReclaimDoesNotConsumeARetryAttempt(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-attempt", "t1", "worker-gone", storage.TaskStatusRunning)

	before, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)

	coord.ReclaimOrphanedTasks(ctx)

	after, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, before.Attempt, after.Attempt,
		"a restart is not a retry: the attempt budget belongs to the retry policy")
	assert.Equal(t, before.MaxAttempts, after.MaxAttempts)
}

// The pass is idempotent: running it twice changes nothing the second time.
//
// It runs both at startup and on gaining leadership, and those can both happen
// in one process lifetime.
func TestReclaimIsIdempotent(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-idem", "t1", "worker-gone", storage.TaskStatusRunning)

	coord.ReclaimOrphanedTasks(ctx)
	first, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)

	coord.ReclaimOrphanedTasks(ctx)
	second, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)

	assert.Equal(t, first.Status, second.Status)
	assert.Equal(t, storage.TaskStatusReady, second.Status)
}

// The reclaim is recorded as an event, WITH the reason.
//
// Afterwards, "why did this task run twice" must be answerable from the
// workflow's own history rather than by correlating timestamps with log lines.
func TestReclaimIsRecordedWithItsReason(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-event", "t1", "worker-gone", storage.TaskStatusRunning)

	coord.ReclaimOrphanedTasks(ctx)

	events, err := coord.store.GetWorkflowHistory(ctx, "wf-event")
	require.NoError(t, err)

	var retry *storage.Event
	for _, e := range events {
		if e.Type == storage.EventTaskRetrying {
			retry = e
			break
		}
	}
	require.NotNil(t, retry, "the reclaim must be visible in the workflow history")

	require.NotEmpty(t, retry.Payload, "and it must carry the reason")
	assert.Contains(t, string(retry.Payload), "restarted",
		"the reason distinguishes a restart from a worker death")
	assert.Contains(t, string(retry.Payload), "worker-gone",
		"and names the worker that could not be accounted for")
}

// --- the dead-worker path is unchanged ---

// The original trigger still works, and now shares its implementation with the
// restart path.
func TestDeadWorkerTasksAreStillReclaimed(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-dead", "t1", "worker-dead", storage.TaskStatusRunning)

	coord.rescheduleDeadWorkerTasks("worker-dead")

	task, err := coord.store.GetTask(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, task.Status)
}

// The dead-worker pass is scoped to ITS worker: another worker's task is not
// collateral damage.
func TestDeadWorkerPassDoesNotTouchOtherWorkers(t *testing.T) {
	coord := newReclaimCoordinator(t)
	ctx := context.Background()

	seedRunningTask(t, coord, "wf-scope", "dead-task", "worker-dead", storage.TaskStatusRunning)
	seedRunningTask(t, coord, "wf-scope", "other-task", "worker-other", storage.TaskStatusRunning)

	coord.rescheduleDeadWorkerTasks("worker-dead")

	dead, err := coord.store.GetTask(ctx, "dead-task")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, dead.Status)

	// worker-other is not registered either, but the dead-worker pass is about
	// ONE worker and must not widen into a general sweep.
	other, err := coord.store.GetTask(ctx, "other-task")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusRunning, other.Status,
		"a pass about one worker must not reclaim another's tasks")
}

// A failed store lookup must not prevent the rest of the pass, and must not
// panic. Exercised by passing a cancelled context, which makes the list fail.
func TestReclaimWithAFailingStoreDoesNotPanic(t *testing.T) {
	coord := newReclaimCoordinator(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.NotPanics(t, func() { coord.ReclaimOrphanedTasks(ctx) })
}
