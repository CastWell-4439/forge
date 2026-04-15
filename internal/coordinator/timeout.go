package coordinator

import (
	"context"
	"log"
	"time"

	"github.com/castwell/forge/internal/storage"
)

const (
	// timeoutCheckInterval is how often the timeout manager scans for expired tasks/workflows.
	timeoutCheckInterval = 5 * time.Second
)

// TimeoutManager monitors running tasks and workflows for timeout expiry.
// When a task exceeds its deadline, it is marked FAILED and retry logic is triggered.
// When a workflow exceeds its deadline, it is marked FAILED immediately.
type TimeoutManager struct {
	store storage.Storage
	// onTaskTimeout is called when a task times out.
	// The coordinator sets this to trigger retry or permanent failure.
	onTaskTimeout func(ctx context.Context, task *storage.Task)
}

// NewTimeoutManager creates a new TimeoutManager.
func NewTimeoutManager(store storage.Storage) *TimeoutManager {
	return &TimeoutManager{store: store}
}

// OnTaskTimeout sets the callback invoked when a task exceeds its timeout.
func (tm *TimeoutManager) OnTaskTimeout(fn func(ctx context.Context, task *storage.Task)) {
	tm.onTaskTimeout = fn
}

// Run starts the background timeout checking loop. It blocks until ctx is cancelled.
func (tm *TimeoutManager) Run(ctx context.Context) {
	ticker := time.NewTicker(timeoutCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tm.checkTimeouts(ctx)
		}
	}
}

// checkTimeouts scans running tasks and workflows for timeout violations.
func (tm *TimeoutManager) checkTimeouts(ctx context.Context) {
	tm.checkTaskTimeouts(ctx)
	tm.checkWorkflowTimeouts(ctx)
}

// checkTaskTimeouts finds RUNNING tasks that have exceeded their TimeoutAt.
func (tm *TimeoutManager) checkTaskTimeouts(ctx context.Context) {
	// List all running workflows to find their tasks
	workflows, err := tm.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, 1000, 0)
	if err != nil {
		log.Printf("ERROR: timeout manager list running workflows: %v", err)
		return
	}

	now := time.Now()
	for _, wf := range workflows {
		tasks, err := tm.store.ListTasksByWorkflow(ctx, wf.ID)
		if err != nil {
			log.Printf("ERROR: timeout manager list tasks for workflow %s: %v", wf.ID, err)
			continue
		}

		for _, task := range tasks {
			if task.Status != storage.TaskStatusRunning {
				continue
			}
			if task.TimeoutAt == nil {
				continue
			}
			if now.After(*task.TimeoutAt) {
				log.Printf("INFO: task %s timed out (deadline=%s, now=%s)",
					task.ID, task.TimeoutAt.Format(time.RFC3339), now.Format(time.RFC3339))

				if err := tm.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusFailed); err != nil {
					log.Printf("ERROR: timeout manager update task %s to FAILED: %v", task.ID, err)
					continue
				}

				if tm.onTaskTimeout != nil {
					tm.onTaskTimeout(ctx, task)
				}
			}
		}
	}
}

// checkWorkflowTimeouts finds RUNNING workflows that have exceeded their TimeoutAt.
func (tm *TimeoutManager) checkWorkflowTimeouts(ctx context.Context) {
	workflows, err := tm.store.ListWorkflows(ctx, storage.WorkflowStatusRunning, 1000, 0)
	if err != nil {
		log.Printf("ERROR: timeout manager list running workflows: %v", err)
		return
	}

	now := time.Now()
	for _, wf := range workflows {
		if wf.TimeoutAt == nil {
			continue
		}
		if now.After(*wf.TimeoutAt) {
			log.Printf("INFO: workflow %s timed out (deadline=%s, now=%s)",
				wf.ID, wf.TimeoutAt.Format(time.RFC3339), now.Format(time.RFC3339))

			if err := tm.store.UpdateWorkflowStatus(ctx, wf.ID, storage.WorkflowStatusFailed); err != nil {
				log.Printf("ERROR: timeout manager update workflow %s to FAILED: %v", wf.ID, err)
			}
		}
	}
}

// TaskContextWithTimeout creates a context with the task's timeout duration.
// Returns the context and cancel function. If the task has no timeout, returns
// a context derived from the parent without a deadline.
func TaskContextWithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
