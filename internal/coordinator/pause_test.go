package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// newPauseTestCoordinator wires a coordinator on a temp Bolt store with one
// workflow whose single task sits in the given state.
func newPauseTestCoordinator(t *testing.T, taskStatus storage.TaskStatus) (*Coordinator, string, string) {
	t.Helper()
	store, err := storage.NewBoltStorage(t.TempDir() + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	wf := &storage.Workflow{ID: "wf-pause", Name: "pause-test", Status: storage.WorkflowStatusRunning,
		CreatedAt: time.Now()}
	require.NoError(t, store.SaveWorkflow(ctx, wf))

	task := &storage.Task{
		ID: "task-pause", WorkflowID: wf.ID, TaskName: "t", Handler: "shell",
		Status: taskStatus, WorkerID: "worker-1", CreatedAt: time.Now(),
	}
	require.NoError(t, store.SaveTask(ctx, task))

	return NewCoordinator(store), wf.ID, task.ID
}

// A paused task is parked, not failed: its status is PAUSED, its workflow says
// so too, and — the part that makes a pause safe — no worker keeps holding it.
func TestOnTaskPausedParksTaskAndReleasesWorker(t *testing.T) {
	c, wfID, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))

	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusPaused, task.Status, "the task waits, it does not fail")
	assert.Empty(t, task.WorkerID, "the worker must be released immediately")

	wf, err := c.store.GetWorkflow(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusPaused, wf.Status,
		"the workflow says waiting-on-human, not still-running")

	// The pause is on the audit trail.
	events, err := c.store.GetWorkflowHistory(ctx, wfID)
	require.NoError(t, err)
	var sawPause bool
	for _, e := range events {
		if e.Type == storage.EventTaskPaused {
			sawPause = true
		}
	}
	assert.True(t, sawPause, "TASK_PAUSED must be recorded")
}

// The closed loop this round exists for: approve, and the task goes back to
// READY where the normal scheduler claims it — no special resume machinery.
func TestResumePausedTaskReturnsToScheduler(t *testing.T) {
	c, wfID, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))
	require.NoError(t, c.ResumePausedTask(ctx, taskID, "operator approved"))

	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, task.Status, "approved tasks re-enter the normal queue")
	assert.Empty(t, task.WorkerID, "READY means claimable, so nobody may own it")

	wf, err := c.store.GetWorkflow(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusRunning, wf.Status)

	// Proof it is genuinely back in the queue: the ordinary claim path takes it.
	claimed, err := c.store.ClaimTask(ctx, "worker-2", []string{"shell"})
	require.NoError(t, err)
	require.NotNil(t, claimed, "the scheduler must be able to re-dispatch an approved task")
	assert.Equal(t, taskID, claimed.ID)
	assert.Equal(t, "worker-2", claimed.WorkerID)
}

// Resuming something that is not paused is an error, not a silent no-op: two
// approvals arriving for one task means the caller's state is wrong and it
// should be told.
func TestResumeRejectsNonPausedTask(t *testing.T) {
	c, _, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	err := c.ResumePausedTask(ctx, taskID, "operator approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not PAUSED")
}

// Rejection takes the ordinary failure path: a refused task is a failed task.
func TestRejectPausedTaskFailsIt(t *testing.T) {
	c, _, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))
	require.NoError(t, c.RejectPausedTask(ctx, taskID, "denied by operator"))

	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusFailed, task.Status)
	assert.Contains(t, task.ErrorMsg, "denied by operator")
}

// The timeout sweeper must not kill a task that is waiting on a human: a
// deadline passing while a review is pending is not an execution failure.
// (Safe today because the sweeper only scans RUNNING — this test pins it.)
func TestPausedTaskSurvivesTimeoutSweep(t *testing.T) {
	c, wfID, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	// An already-expired deadline, so any accidental sweep would fire.
	past := time.Now().Add(-time.Hour)
	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	task.TimeoutAt = &past
	require.NoError(t, c.store.SaveTask(ctx, task))

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))

	tm := NewTimeoutManager(c.store)
	tm.checkTimeouts(ctx)

	stored, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusPaused, stored.Status,
		"an expired deadline must not fail a task that is merely waiting")

	wf, err := c.store.GetWorkflow(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusPaused, wf.Status)
}

// Approving a long-waited task must not put it straight into the sweeper's
// sights: the original deadline (computed at creation) is re-based so the
// approved task gets its full execution window again.
func TestResumeRebasesExpiredDeadline(t *testing.T) {
	c, _, taskID := newPauseTestCoordinator(t, storage.TaskStatusRunning)
	ctx := context.Background()

	// Deadline = creation + 1h, already expired, i.e. created 2h ago.
	task, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	task.CreatedAt = time.Now().Add(-2 * time.Hour)
	expired := time.Now().Add(-1 * time.Hour)
	task.TimeoutAt = &expired
	require.NoError(t, c.store.SaveTask(ctx, task))

	require.NoError(t, c.OnTaskPaused(ctx, taskID, "needs approval"))
	require.NoError(t, c.ResumePausedTask(ctx, taskID, "operator approved"))

	resumed, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, resumed.TimeoutAt, "the deadline stays, rebased")
	assert.True(t, resumed.TimeoutAt.After(time.Now()),
		"an approved task must not be immediately expired (deadline=%v)", resumed.TimeoutAt)
	// The original duration was 1h; a rebase must preserve roughly that scale.
	assert.WithinDuration(t, time.Now().Add(time.Hour), *resumed.TimeoutAt, 5*time.Minute,
		"the original 1h window is preserved")

	// And the sweeper leaves it alone right after approval.
	NewTimeoutManager(c.store).checkTimeouts(ctx)
	after, err := c.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, after.Status,
		"a freshly approved task must survive the sweep")
}
