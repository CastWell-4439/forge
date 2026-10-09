package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// Pause and resume: the missing half of the runtime gate.
//
// The gate could already say "this task needs human approval" (GateActionPause),
// but the runtime had nowhere to put such a task: TaskStatus had no PAUSED and
// the executor reported a held task as a plain failure. The worker's own comment
// recorded the consequence — "the engine has no paused/resumed task state, so
// pause currently has the same runtime effect as block" — and deferring without
// a resume path would have deadlocked the task.
//
// The shape here is deliberately "release and re-queue", not "suspend in place":
//
//	pause  -> task PAUSED, no worker owns it, the worker is free immediately
//	approve -> task READY again, the ordinary scheduler claims it as usual
//	reject  -> task FAILED, the ordinary failure path reports it
//
// Re-queueing is what makes the pause safe: nothing holds a worker slot, and
// nothing has to remember to wake it up — the existing claim loop does.

// OnTaskPaused parks a task that the runtime gate held for human approval.
//
// The task is moved to PAUSED and its worker assignment is cleared so the
// scheduler stops considering it owned. The reason travels into the event log
// — that record, plus ResolveTaskPause, is the audit trail of the wait.
func (c *Coordinator) OnTaskPaused(ctx context.Context, taskID, reason string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}

	if err := c.store.UpdateTaskStatus(ctx, taskID, storage.TaskStatusPaused); err != nil {
		return fmt.Errorf("pause task %s: %w", taskID, err)
	}

	// Release the worker: a paused task must not occupy a capacity slot, and
	// the re-dispatch on approval goes through the normal claim path.
	c.clearTaskWorker(ctx, taskID)

	// The workflow is paused too, so an operator looking at the workflow sees
	// "waiting on a human" rather than a workflow that is still "running".
	if task.WorkflowID != "" {
		if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusPaused); err != nil {
			log.Printf("WARN: mark workflow %s paused: %v", task.WorkflowID, err)
		}
	}

	payload, _ := json.Marshal(map[string]any{"reason": reason})
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskPaused, payload)
	log.Printf("INFO: task %s paused awaiting human approval: %s", taskID, reason)
	return nil
}

// ResumePausedTask returns a paused task to READY so the ordinary scheduler
// re-dispatches it. The caller is the human decision; reason is recorded in
// the resume event — an approval without its reason leaves evidence nobody can
// reconstruct later.
//
// A task that is not PAUSED is an error: resuming something that is not
// paused usually means the approval arrived twice, and the caller's view is
// stale.
func (c *Coordinator) ResumePausedTask(ctx context.Context, taskID, reason string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}
	if task.Status != storage.TaskStatusPaused {
		return fmt.Errorf("task %s is %s, not PAUSED", taskID, task.Status)
	}
	if reason == "" {
		reason = "approved by human review"
	}

	if err := c.store.UpdateTaskStatus(ctx, taskID, storage.TaskStatusReady); err != nil {
		return fmt.Errorf("resume task %s: %w", taskID, err)
	}
	// Clear the previous claim so the scheduler can pick it up again.
	c.clearTaskWorker(ctx, taskID)

	// Re-base the deadline: the original timeout_at was computed at creation,
	// so a task that waited hours for an approval would come back already
	// expired and die in the sweeper the moment it is approved. The original
	// duration (timeout_at - created_at) restarts from now. A task without both
	// timestamps keeps no deadline rather than inventing one.
	if task.TimeoutAt != nil && !task.CreatedAt.IsZero() {
		duration := task.TimeoutAt.Sub(task.CreatedAt)
		if duration > 0 {
			deadline := time.Now().Add(duration)
			if err := c.store.RebaseTaskDeadline(ctx, taskID, &deadline); err != nil {
				log.Printf("WARN: rebase deadline on task %s: %v", taskID, err)
			}
		}
	}

	if task.WorkflowID != "" {
		if err := c.store.UpdateWorkflowStatus(ctx, task.WorkflowID, storage.WorkflowStatusRunning); err != nil {
			log.Printf("WARN: mark workflow %s running: %v", task.WorkflowID, err)
		}
	}

	payload, _ := json.Marshal(map[string]any{"reason": reason})
	c.saveEvent(ctx, task.WorkflowID, taskID, storage.EventTaskResumed, payload)
	log.Printf("INFO: task %s resumed after approval (%s); the scheduler will re-dispatch it", taskID, reason)
	return nil
}

// RejectPausedTask fails a paused task after a rejected review, through the
// ordinary failure path so the workflow's error handling stays in one place.
func (c *Coordinator) RejectPausedTask(ctx context.Context, taskID, reason string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}
	if task.Status != storage.TaskStatusPaused {
		return fmt.Errorf("task %s is %s, not PAUSED", taskID, task.Status)
	}
	if reason == "" {
		reason = "rejected by human review"
	}
	return c.OnTaskFailed(ctx, taskID, reason)
}

// FailPausedTaskOnTimeout ends a pause that nobody resolved in time.
//
// A request that expires has to land somewhere. Leaving the task parked would
// mean a workflow waiting on an approval that can no longer arrive, with nothing
// in the system willing to say so — the stall would look identical to a task
// that is merely slow.
//
// It goes through the ordinary failure path so the workflow's own failure
// handling runs, exactly as a rejected review does. The difference between
// "rejected" and "nobody answered" is in the reason, which is what an operator
// reads.
func (c *Coordinator) FailPausedTaskOnTimeout(ctx context.Context, taskID string) error {
	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %s: %w", taskID, err)
	}
	if task.Status != storage.TaskStatusPaused {
		// It moved on: answered at the last moment, or already failed. Nothing
		// to do, and the caller is told so it does not retry forever.
		return fmt.Errorf("task %s is %s, not PAUSED", taskID, task.Status)
	}
	return c.OnTaskFailed(ctx, taskID, "human approval request timed out")
}

// ResolveTaskPause records a human decision about a paused task — the
// production entry point for approval. State lives in the coordinator, so the
// approval API lives here too: asking a second system to approve something it
// cannot inspect is how the two halves drift apart.
//
// The response carries the new status so the caller can confirm the
// transition rather than infer it from a 200.
func (c *Coordinator) ResolveTaskPause(ctx context.Context, req *forgev1.ResolveTaskPauseRequest) (*forgev1.ResolveTaskPauseResponse, error) {
	taskID := req.GetTaskId()
	if taskID == "" {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}

	reason := req.GetReason()
	var err error
	if req.GetApprove() {
		err = c.ResumePausedTask(ctx, taskID, reason)
	} else {
		if reason == "" {
			reason = "rejected by human review"
		}
		err = c.RejectPausedTask(ctx, taskID, reason)
	}
	if err != nil {
		// "not PAUSED" means the caller's view is stale (double resolve, task
		// already advanced) — a conflict, not a server fault.
		if strings.Contains(err.Error(), "not PAUSED") {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	task, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get task %s: %v", taskID, err)
	}
	return &forgev1.ResolveTaskPauseResponse{
		Status:    taskStatusToProto(task.Status),
		NewStatus: string(task.Status),
	}, nil
}

// clearTaskWorker drops the task's worker assignment so the scheduler stops
// treating it as owned. It runs after every state change that takes a task out
// of a worker's hands, so a released task never keeps a capacity slot.
func (c *Coordinator) clearTaskWorker(ctx context.Context, taskID string) {
	if err := c.store.ReleaseTask(ctx, taskID); err != nil {
		log.Printf("WARN: release task %s: %v", taskID, err)
	}
}
