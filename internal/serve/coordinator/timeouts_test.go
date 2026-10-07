package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
)

// The timeout manager was written but never assembled, so no task ever failed
// for exceeding its deadline. These tests pin the behaviour the assembly now
// turns on, and the Kueue interaction that depends on it.

// A RUNNING task past its deadline is failed. This is the behavioural change
// the assembly enables: previously such a task stayed RUNNING forever.
func TestTimeoutManagerFailsExpiredTask(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.NoError(t, store.SaveWorkflow(ctx, &storage.Workflow{
		ID: "wf-timeout", Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	past := time.Now().Add(-time.Minute)
	require.NoError(t, store.SaveTask(ctx, &storage.Task{
		ID: "task-late", WorkflowID: "wf-timeout", TaskName: "t", Handler: "h",
		Status: storage.TaskStatusRunning, TimeoutAt: &past, CreatedAt: time.Now().UTC(),
	}))

	mgr := coordinator.NewTimeoutManager(store)
	timedOut := make(chan string, 1)
	mgr.OnTaskTimeout(func(_ context.Context, task *storage.Task) {
		select {
		case timedOut <- task.ID:
		default:
		}
	})

	// Drive one scan by starting the loop and waiting for the callback; the
	// loop's own interval is short enough for a test.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go mgr.Run(runCtx)

	select {
	case id := <-timedOut:
		assert.Equal(t, "task-late", id)
	case <-time.After(15 * time.Second):
		t.Fatal("the timeout callback never fired for an expired task")
	}

	task, err := store.GetTask(ctx, "task-late")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusFailed, task.Status, "the expired task was failed")
}

// A task that is not RUNNING is left alone however old its deadline is: the
// scan is about in-flight work, not history.
func TestTimeoutManagerIgnoresFinishedTasks(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.NoError(t, store.SaveWorkflow(ctx, &storage.Workflow{
		ID: "wf-done", Name: "wf", Status: storage.WorkflowStatusRunning, CreatedAt: time.Now().UTC(),
	}))
	past := time.Now().Add(-time.Hour)
	require.NoError(t, store.SaveTask(ctx, &storage.Task{
		ID: "task-done", WorkflowID: "wf-done", TaskName: "t", Handler: "h",
		Status: storage.TaskStatusCompleted, TimeoutAt: &past, CreatedAt: time.Now().UTC(),
	}))

	mgr := coordinator.NewTimeoutManager(store)
	fired := make(chan struct{}, 1)
	mgr.OnTaskTimeout(func(context.Context, *storage.Task) { fired <- struct{}{} })

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go mgr.Run(runCtx)
	time.Sleep(7 * time.Second)

	select {
	case <-fired:
		t.Fatal("a completed task must not be timed out")
	default:
	}

	task, err := store.GetTask(ctx, "task-done")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusCompleted, task.Status, "the status is untouched")
}
