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
