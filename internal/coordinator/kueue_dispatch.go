package coordinator

import (
	"context"
	"log"
	"time"

	"github.com/castwell/forge/internal/storage"
)

// dispatchKueueTask submits a GPU task to Kueue and returns immediately.
//
// The task is marked RUNNING with the job's identity recorded in WorkerID, so
// the reconciler can find it after a restart from storage alone — no extra
// column, no in-memory registry to lose. Its deadline goes into TimeoutAt,
// which the timeout manager already scans, so a job that never finishes is
// failed by the same machinery that fails any other stuck task.
//
// A submission failure fails the task, exactly as a failed render does: the
// task is READY and cannot progress, so saying so is better than leaving it
// RUNNING against a job that does not exist.
func (c *Coordinator) dispatchKueueTask(ctx context.Context, task *storage.Task, input []byte) {
	taskDef := c.taskDef(task)

	if err := c.kueue.SubmitGPUTask(ctx, task.WorkflowID, task.ID, taskDef); err != nil {
		log.Printf("ERROR: kueue submit task %s: %v", task.ID, err)
		if failErr := c.OnTaskFailed(ctx, task.ID, "kueue submit: "+err.Error()); failErr != nil {
			log.Printf("ERROR: handle task %s kueue submit failure: %v", task.ID, failErr)
		}
		return
	}

	jobName := c.kueue.JobName(task.WorkflowID, task.ID)
	namespace := c.kueue.Namespace()

	// Mark running before recording the sentinel: a task that is RUNNING with
	// a worker id and no job is recoverable by inspection, while a job nobody
	// tracks is not.
	if err := c.store.UpdateTaskStatus(ctx, task.ID, storage.TaskStatusRunning); err != nil {
		log.Printf("ERROR: update kueue task %s to running: %v", task.ID, err)
		return
	}

	// The sentinel plus the deadline are what make this task reconcilable and
	// timeout-able. Both are written through the narrow accessors rather than
	// SaveTask: on PostgreSQL SaveTask is a plain INSERT, so using it to amend
	// an existing row fails on the primary key and silently leaves the task
	// with neither its owner nor its deadline — which is exactly the state
	// that makes a GPU task unreconcilable.
	if err := c.store.AssignTaskWorker(ctx, task.ID, kueueWorkerID(namespace, jobName)); err != nil {
		log.Printf("ERROR: record kueue identity for task %s: %v", task.ID, err)
	}
	if deadline := kueueDeadline(taskDef, time.Now().UTC()); deadline != nil {
		if err := c.store.RebaseTaskDeadline(ctx, task.ID, deadline); err != nil {
			log.Printf("ERROR: record kueue deadline for task %s: %v", task.ID, err)
		}
	}

	c.saveEvent(ctx, task.WorkflowID, task.ID, storage.EventTaskStarted, nil)
	log.Printf("INFO: kueue: task %s dispatched as job %s/%s (async; results reported by the reconciler)",
		task.ID, namespace, jobName)
}

// taskDef resolves a task's definition from the cached DAG. A task whose DAG
// is not cached (a resumed run whose definition was not reloaded) yields a
// zero TaskDef: IsGPUTask then reports false and the task takes the ordinary
// worker path rather than being guessed at.
func (c *Coordinator) taskDef(task *storage.Task) TaskDef {
	c.dagCacheMu.RLock()
	dag := c.dagCache[task.WorkflowID]
	c.dagCacheMu.RUnlock()
	if dag == nil {
		return TaskDef{}
	}
	if def, ok := dag.Tasks[task.TaskName]; ok && def != nil {
		return *def
	}
	return TaskDef{}
}

// kueueDeadline is when a job must be finished by. The task's own timeout wins
// when set; otherwise the manager's default TTL applies, so a GPU task is
// never left without a deadline.
func kueueDeadline(taskDef TaskDef, now time.Time) *time.Time {
	ttl := taskDef.Timeout
	if ttl <= 0 {
		ttl = DefaultKueueConfig().DefaultTTL
	}
	if ttl <= 0 {
		return nil
	}
	deadline := now.Add(ttl)
	return &deadline
}
