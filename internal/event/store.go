// Package event implements event sourcing for Forge workflows.
// It provides event storage helpers and workflow state replay from event history.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/castwell/forge/internal/storage"
)

// WorkflowState represents the state of a workflow reconstructed from events.
type WorkflowState struct {
	WorkflowID string
	Status     storage.WorkflowStatus
	Tasks      map[string]*TaskState // task_name -> state
	StartedAt  *time.Time
	FinishedAt *time.Time
	ErrorMsg   string
	Events     []storage.Event // all events in order
}

// TaskState represents the state of a task reconstructed from events.
type TaskState struct {
	TaskID     string
	TaskName   string
	Status     storage.TaskStatus
	Output     json.RawMessage
	ErrorMsg   string
	Attempts   int
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// Store wraps the low-level storage.Storage event methods with
// higher-level helpers for the event sourcing pattern.
type Store struct {
	storage storage.Storage
}

// NewStore creates a new event Store.
func NewStore(s storage.Storage) *Store {
	return &Store{storage: s}
}

// Append persists a new event. It auto-sets Timestamp if zero.
func (s *Store) Append(ctx context.Context, event *storage.Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	return s.storage.SaveEvent(ctx, event)
}

// History retrieves all events for a workflow, ordered by sequence number.
func (s *Store) History(ctx context.Context, workflowID string) ([]*storage.Event, error) {
	return s.storage.GetWorkflowHistory(ctx, workflowID)
}

// Replay reconstructs the workflow state from its event history.
// This is the core event sourcing primitive: given a sequence of immutable events,
// we can reconstruct the exact state at any point.
func Replay(events []*storage.Event) (*WorkflowState, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("no events to replay")
	}

	// Sort by sequence number to ensure correct order.
	sort.Slice(events, func(i, j int) bool {
		return events[i].SequenceNum < events[j].SequenceNum
	})

	state := &WorkflowState{
		WorkflowID: events[0].WorkflowID,
		Status:     storage.WorkflowStatusPending,
		Tasks:      make(map[string]*TaskState),
	}

	for _, event := range events {
		if err := applyEvent(state, event); err != nil {
			return nil, fmt.Errorf("apply event seq=%d type=%s: %w", event.SequenceNum, event.Type, err)
		}
		state.Events = append(state.Events, *event)
	}

	return state, nil
}

// ReplayUntil reconstructs the workflow state up to the given sequence number.
// Useful for "time travel" debugging.
func ReplayUntil(events []*storage.Event, maxSeq int64) (*WorkflowState, error) {
	filtered := make([]*storage.Event, 0, len(events))
	for _, e := range events {
		if e.SequenceNum <= maxSeq {
			filtered = append(filtered, e)
		}
	}
	return Replay(filtered)
}

// CompletedTasks returns the names of tasks that reached COMPLETED status.
func (s *WorkflowState) CompletedTasks() []string {
	var result []string
	for name, ts := range s.Tasks {
		if ts.Status == storage.TaskStatusCompleted {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

// FailedTasks returns the names of tasks that reached FAILED status.
func (s *WorkflowState) FailedTasks() []string {
	var result []string
	for name, ts := range s.Tasks {
		if ts.Status == storage.TaskStatusFailed {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

// applyEvent mutates the WorkflowState based on a single event.
func applyEvent(state *WorkflowState, event *storage.Event) error {
	ts := event.Timestamp

	switch event.Type {
	case storage.EventWorkflowSubmitted:
		state.Status = storage.WorkflowStatusPending

	case storage.EventWorkflowStarted:
		state.Status = storage.WorkflowStatusRunning
		state.StartedAt = &ts

	case storage.EventWorkflowCompleted:
		state.Status = storage.WorkflowStatusCompleted
		state.FinishedAt = &ts

	case storage.EventWorkflowFailed:
		state.Status = storage.WorkflowStatusFailed
		state.FinishedAt = &ts
		if event.Payload != nil {
			var p map[string]string
			if err := json.Unmarshal(event.Payload, &p); err == nil {
				state.ErrorMsg = p["error"]
			}
		}

	case storage.EventTaskScheduled:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusScheduled

	case storage.EventTaskStarted:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusRunning
		taskState.StartedAt = &ts
		taskState.Attempts++

	case storage.EventTaskCompleted:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusCompleted
		taskState.FinishedAt = &ts
		if event.Payload != nil {
			taskState.Output = event.Payload
		}

	case storage.EventTaskFailed:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusFailed
		taskState.FinishedAt = &ts
		if event.Payload != nil {
			var p map[string]string
			if err := json.Unmarshal(event.Payload, &p); err == nil {
				taskState.ErrorMsg = p["error"]
			}
		}

	case storage.EventTaskRetrying:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusPending
		// Attempt count incremented on next TaskStarted

	case storage.EventTaskCompensating:
		taskState := state.getOrCreateTask(event.TaskID)
		taskState.Status = storage.TaskStatusCompensating

	default:
		// Unknown event type — skip silently for forward compatibility.
	}

	return nil
}

// getOrCreateTask returns the TaskState for a given task ID, creating it if needed.
func (s *WorkflowState) getOrCreateTask(taskID string) *TaskState {
	// We index by TaskID since events don't always carry TaskName.
	if ts, ok := s.Tasks[taskID]; ok {
		return ts
	}
	ts := &TaskState{TaskID: taskID}
	s.Tasks[taskID] = ts
	return ts
}
