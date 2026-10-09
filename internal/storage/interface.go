// Package storage defines the persistence interface for Forge and provides
// domain types for workflows, tasks, and events.
package storage

import (
	"context"
	"encoding/json"
	"time"
)

// WorkflowStatus represents the lifecycle state of a workflow instance.
type WorkflowStatus string

const (
	WorkflowStatusPending      WorkflowStatus = "PENDING"
	WorkflowStatusRunning      WorkflowStatus = "RUNNING"
	WorkflowStatusCompleted    WorkflowStatus = "COMPLETED"
	WorkflowStatusFailed       WorkflowStatus = "FAILED"
	WorkflowStatusCancelled    WorkflowStatus = "CANCELLED"
	WorkflowStatusCompensating WorkflowStatus = "COMPENSATING"
	// WorkflowStatusPaused means the workflow is waiting on a human decision.
	// It is NOT terminal: approving the pending review moves it back to
	// RUNNING, rejecting moves it to FAILED.
	WorkflowStatusPaused WorkflowStatus = "PAUSED"
)

// TaskStatus represents the lifecycle state of a task instance.
type TaskStatus string

const (
	TaskStatusPending      TaskStatus = "PENDING"
	TaskStatusReady        TaskStatus = "READY"
	TaskStatusScheduled    TaskStatus = "SCHEDULED"
	TaskStatusRunning      TaskStatus = "RUNNING"
	TaskStatusCompleted    TaskStatus = "COMPLETED"
	TaskStatusFailed       TaskStatus = "FAILED"
	TaskStatusSkipped      TaskStatus = "SKIPPED"
	TaskStatusCompensating TaskStatus = "COMPENSATING"
	// TaskStatusPaused means the task was held for human approval before its
	// handler ran. The worker is released (the task is not claimed by anyone);
	// approval moves it back to READY so the normal scheduler re-dispatches it,
	// rejection moves it to FAILED. Deferring without this state would either
	// occupy a worker forever or collapse into FAILED — which is what the gate
	// used to do.
	TaskStatusPaused TaskStatus = "PAUSED"
)

// EventType defines the type of workflow/task event.
type EventType string

const (
	EventWorkflowSubmitted EventType = "WORKFLOW_SUBMITTED"
	EventWorkflowStarted   EventType = "WORKFLOW_STARTED"
	EventWorkflowCompleted EventType = "WORKFLOW_COMPLETED"
	EventWorkflowFailed    EventType = "WORKFLOW_FAILED"
	EventTaskScheduled     EventType = "TASK_SCHEDULED"
	EventTaskStarted       EventType = "TASK_STARTED"
	EventTaskCompleted     EventType = "TASK_COMPLETED"
	EventTaskFailed        EventType = "TASK_FAILED"
	EventTaskRetrying      EventType = "TASK_RETRYING"
	EventTaskCompensating  EventType = "TASK_COMPENSATING"
	// EventTaskPaused / EventTaskResumed record the approval wait. They are
	// distinct from the failure and retry events because being held for a human
	// is neither: the task has not failed and has not been retried.
	EventTaskPaused  EventType = "TASK_PAUSED"
	EventTaskResumed EventType = "TASK_RESUMED"
	// EventTaskSkipped records a task that never ran because its own condition
	// evaluated to false. The audit trail has to be able to answer "why did
	// this task not run" with something better than its absence.
	EventTaskSkipped EventType = "TASK_SKIPPED"
	// EventTaskGoto records a loop iteration: which task jumped to which, and
	// how many times the target had been revisited. Without it a replay would
	// show a task running repeatedly with nothing explaining why.
	EventTaskGoto EventType = "TASK_GOTO"
	// EventTaskRewound records a task returning to the queue because a goto
	// jumped back over it.
	EventTaskRewound EventType = "TASK_REWOUND"
)

// WorkflowDefinition stores a versioned workflow DAG definition.
type WorkflowDefinition struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	DagYAML   json.RawMessage `json:"dag_yaml"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Workflow represents a workflow instance (a single execution).
type Workflow struct {
	ID         string          `json:"id"`
	DefID      int64           `json:"def_id"`
	Name       string          `json:"name"`
	Status     WorkflowStatus  `json:"status"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	ErrorMsg   string          `json:"error_msg"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	CreatedAt  time.Time       `json:"created_at"`
	TimeoutAt  *time.Time      `json:"timeout_at"`
}

// Task represents a task instance within a workflow execution.
type Task struct {
	ID          string          `json:"id"`
	WorkflowID  string          `json:"workflow_id"`
	TaskName    string          `json:"task_name"`
	Handler     string          `json:"handler"`
	Status      TaskStatus      `json:"status"`
	WorkerID    string          `json:"worker_id"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output"`
	ErrorMsg    string          `json:"error_msg"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"max_attempts"`
	ScheduledAt *time.Time      `json:"scheduled_at"`
	StartedAt   *time.Time      `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at"`
	TimeoutAt   *time.Time      `json:"timeout_at"`
	CreatedAt   time.Time       `json:"created_at"`
	DependsOn   []string        `json:"depends_on"`

	// LoopIteration counts how many times this task has been revisited by an
	// `on_result: goto` route. It is persisted because it is the only thing
	// bounding a goto loop: an in-memory counter would reset on restart and
	// turn a capped loop into an unbounded one.
	//
	// It is separate from Attempt, which counts execution attempts for the
	// retry policy. One goto may cause several attempts, and a retry does not
	// consume loop budget; folding them together would make each one silently
	// change the other's limit.
	LoopIteration int `json:"loop_iteration"`
}

// Event represents an immutable event in the event sourcing log.
type Event struct {
	ID          int64           `json:"id"`
	WorkflowID  string          `json:"workflow_id"`
	TaskID      string          `json:"task_id"`
	Type        EventType       `json:"event_type"`
	Payload     json.RawMessage `json:"payload"`
	Timestamp   time.Time       `json:"timestamp"`
	SequenceNum int64           `json:"sequence_num"`
}

// Storage defines the persistence interface for Forge.
// Production: PostgreSQL implementation. Dev/standalone: BoltDB implementation.
type Storage interface {
	// Workflow definition CRUD
	SaveWorkflowDefinition(ctx context.Context, def *WorkflowDefinition) error
	GetWorkflowDefinition(ctx context.Context, name string, version int) (*WorkflowDefinition, error)

	// Workflow instance CRUD
	SaveWorkflow(ctx context.Context, wf *Workflow) error
	GetWorkflow(ctx context.Context, workflowID string) (*Workflow, error)
	ListWorkflows(ctx context.Context, status WorkflowStatus, limit int, offset int) ([]*Workflow, error)
	UpdateWorkflowStatus(ctx context.Context, workflowID string, status WorkflowStatus) error

	// Task instance CRUD
	SaveTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, taskID string) (*Task, error)
	ListTasksByWorkflow(ctx context.Context, workflowID string) ([]*Task, error)
	ClaimTask(ctx context.Context, workerID string, handlers []string) (*Task, error)
	UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus) error

	// MarkTaskRunning records that a task has begun executing under a worker:
	// status RUNNING, owner workerID, and the attempt counter advanced.
	//
	// These three belong in one write because they are one fact — "this attempt
	// started". Splitting them means a crash between the writes leaves a task
	// that is running under an owner nobody recorded, or one whose attempt
	// count disagrees with how many times it has actually run.
	//
	// Attempt counts attempts STARTED, which is the reading retry.go's
	// ShouldRetry assumes (Attempt < MaxAttempts = "tries remain"). That makes
	// max_attempts the total number of runs, not one more than it.
	//
	// The owner is what the dead-worker path matches on when it looks for work
	// to requeue; without it a task whose worker died stayed RUNNING forever.
	//
	// A dedicated accessor rather than SaveTask: the PostgreSQL SaveTask is an
	// INSERT, so using it to change fields would insert a duplicate row.
	// A missing task is not an error.
	MarkTaskRunning(ctx context.Context, taskID string, workerID string) error

	// ScheduleTaskRetry returns a failed task to READY with notBefore as the
	// earliest time it may run again.
	//
	// The delay rides on ScheduledAt rather than a new "waiting" status: READY
	// already means "eligible", and a new state would mean teaching every
	// reader of TaskStatus about it. The scheduler treats a READY task whose
	// ScheduledAt is still in the future as not yet eligible — which is the
	// only reading that makes a retry delay mean anything.
	//
	// The attempt counter is NOT advanced here: it counts starts, and this call
	// precedes a start rather than being one.
	//
	// A missing task is not an error.
	ScheduleTaskRetry(ctx context.Context, taskID string, notBefore time.Time) error

	// MarkTaskScheduled moves a task from READY to SCHEDULED, reporting whether
	// this caller was the one that made the transition.
	//
	// It is a compare-and-set rather than a plain status write, and that is the
	// whole point: dispatch begins by reading which tasks are READY, and two
	// schedulers can read the same task before either writes. An unconditional
	// update lets both dispatch it — observed live as the same task reaching a
	// worker twice. Only one caller wins the READY→SCHEDULED transition, so
	// false means "someone else took it" and the caller must not dispatch.
	//
	// Returning a bool rather than an error keeps that distinction: losing the
	// race is normal, not a failure. A missing task also reports false, since
	// there is nothing to dispatch either way.
	//
	// A mutex would not serve here: the coordinator runs as several processes in
	// distributed mode, and only the store is shared.
	MarkTaskScheduled(ctx context.Context, taskID string) (bool, error)

	// RewindTaskForGoto returns a task to the queue because a goto jumped back
	// to it (or to one of its ancestors), and advances its loop counter.
	//
	// This is a rewind, not a retry: the task is being re-run as part of a loop
	// the author asked for, not because it failed. So Attempt is left alone —
	// it belongs to the retry policy, and spending retry budget on a loop
	// iteration would silently change a limit the author set elsewhere.
	//
	// Output is cleared. A re-run produces a new value, and a stale one still
	// readable by successors is worse than no value: it would look like the
	// re-run had already reported. Callers that want the old value should have
	// read it before asking for the rewind.
	//
	// A missing task is not an error.
	RewindTaskForGoto(ctx context.Context, taskID string) error

	// ReleaseTask clears a task's worker assignment so the scheduler no longer
	// treats it as owned. Used when a task is parked (paused awaiting human
	// approval) or returned to READY: the worker must not keep holding a
	// capacity slot, and re-dispatch has to go through the normal claim path.
	// Releasing a task that has no worker is not an error.
	ReleaseTask(ctx context.Context, taskID string) error

	// RebaseTaskDeadline rewrites a task's timeout_at, used when a task resumes
	// after waiting on a human: the deadline was computed at creation and would
	// otherwise be already expired the moment the task is approved (approval
	// would kill it in the sweeper). nil clears the deadline. A missing task is
	// not an error.
	RebaseTaskDeadline(ctx context.Context, taskID string, deadline *time.Time) error

	// CompleteTask marks a task as COMPLETED and persists its output.
	// Also sets finished_at to now. Idempotent: if the task is already in a
	// terminal state (COMPLETED / FAILED / SKIPPED), the call is a no-op and
	// does not return an error. If the task does not exist, the call is also
	// a no-op (a warning is logged).
	CompleteTask(ctx context.Context, taskID string, output json.RawMessage) error

	// FailTask marks a task as FAILED and persists its error message.
	// Also sets finished_at to now. Same idempotent/no-op semantics as
	// CompleteTask.
	FailTask(ctx context.Context, taskID string, errorMsg string) error

	// Event sourcing
	SaveEvent(ctx context.Context, event *Event) error
	GetWorkflowHistory(ctx context.Context, workflowID string) ([]*Event, error)

	// CountWorkflows returns the number of workflows grouped by status.
	// Returns a map of WorkflowStatus → count. Implementations should use
	// an efficient COUNT query rather than loading full rows.
	CountWorkflows(ctx context.Context) (map[WorkflowStatus]int32, error)

	// Lifecycle
	Close() error
}
