package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

func TestReplayEmptyEvents(t *testing.T) {
	_, err := Replay(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no events")
}

// A goto loop replays to the state the loop actually left behind. The state map
// is keyed by task ID, so the assertions do too — and the rewind is driven by the
// per-task TASK_REWOUND events, which is why each rewound task carries one.
func TestReplayGotoLoop(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-g", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-g", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-g", TaskID: "id-check", Type: storage.EventTaskStarted, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-g", TaskID: "id-check", Type: storage.EventTaskCompleted, SequenceNum: 4, Timestamp: now,
			Payload: json.RawMessage(`{"verdict":"retry"}`)},
		{WorkflowID: "wf-g", TaskID: "id-judge", Type: storage.EventTaskStarted, SequenceNum: 5, Timestamp: now},
		{WorkflowID: "wf-g", TaskID: "id-judge", Type: storage.EventTaskCompleted, SequenceNum: 6, Timestamp: now},
		// The jump, then one rewind event per task it reset.
		{WorkflowID: "wf-g", TaskID: "id-judge", Type: storage.EventTaskGoto, SequenceNum: 7, Timestamp: now,
			Payload: json.RawMessage(`{"from":"judge","target":"check","iteration":1}`)},
		{WorkflowID: "wf-g", TaskID: "id-check", Type: storage.EventTaskRewound, SequenceNum: 8, Timestamp: now,
			Payload: json.RawMessage(`{"target":"check","from":"judge","iteration":1}`)},
	}

	state, err := Replay(events)
	require.NoError(t, err)

	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["id-judge"].Status,
		"the task that jumped did complete")
	assert.Equal(t, storage.TaskStatusPending, state.Tasks["id-check"].Status,
		"the target is queued for its next pass, not left completed")
	assert.Nil(t, state.Tasks["id-check"].Output,
		"the stale output must not survive into the replay")
	assert.Equal(t, 1, state.Tasks["id-check"].LoopIterations,
		"the revisit is counted")
}

// The second pass's completion supersedes the first: the output that stands is
// the one produced after the rewind.
func TestReplayRewoundReturnsTaskToTheQueue(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-r", Type: storage.EventWorkflowStarted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-r", TaskID: "id-t", Type: storage.EventTaskCompleted, SequenceNum: 2, Timestamp: now,
			Payload: json.RawMessage(`{"v":1}`)},
		{WorkflowID: "wf-r", TaskID: "id-t", Type: storage.EventTaskRewound, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-r", TaskID: "id-t", Type: storage.EventTaskCompleted, SequenceNum: 4, Timestamp: now,
			Payload: json.RawMessage(`{"v":2}`)},
	}

	state, err := Replay(events)
	require.NoError(t, err)

	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["id-t"].Status)
	assert.Equal(t, 1, state.Tasks["id-t"].LoopIterations)
	assert.JSONEq(t, `{"v":2}`, string(state.Tasks["id-t"].Output),
		"the latest completion's output is the one that stands")
}

func TestReplayWorkflowLifecycle(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-1", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-1", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now.Add(1 * time.Second)},
		{WorkflowID: "wf-1", TaskID: "task-a", Type: storage.EventTaskScheduled, SequenceNum: 3, Timestamp: now.Add(2 * time.Second)},
		{WorkflowID: "wf-1", TaskID: "task-a", Type: storage.EventTaskStarted, SequenceNum: 4, Timestamp: now.Add(3 * time.Second)},
		{WorkflowID: "wf-1", TaskID: "task-a", Type: storage.EventTaskCompleted, SequenceNum: 5, Timestamp: now.Add(4 * time.Second),
			Payload: json.RawMessage(`{"result": "ok"}`)},
		{WorkflowID: "wf-1", TaskID: "task-b", Type: storage.EventTaskScheduled, SequenceNum: 6, Timestamp: now.Add(5 * time.Second)},
		{WorkflowID: "wf-1", TaskID: "task-b", Type: storage.EventTaskStarted, SequenceNum: 7, Timestamp: now.Add(6 * time.Second)},
		{WorkflowID: "wf-1", TaskID: "task-b", Type: storage.EventTaskCompleted, SequenceNum: 8, Timestamp: now.Add(7 * time.Second)},
		{WorkflowID: "wf-1", Type: storage.EventWorkflowCompleted, SequenceNum: 9, Timestamp: now.Add(8 * time.Second)},
	}

	state, err := Replay(events)
	require.NoError(t, err)

	assert.Equal(t, "wf-1", state.WorkflowID)
	assert.Equal(t, storage.WorkflowStatusCompleted, state.Status)
	assert.NotNil(t, state.StartedAt)
	assert.NotNil(t, state.FinishedAt)
	assert.Len(t, state.Tasks, 2)
	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["task-a"].Status)
	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["task-b"].Status)
	assert.Equal(t, 1, state.Tasks["task-a"].Attempts)
	assert.Equal(t, json.RawMessage(`{"result": "ok"}`), state.Tasks["task-a"].Output)
	assert.Len(t, state.Events, 9)

	completed := state.CompletedTasks()
	assert.Len(t, completed, 2)
	assert.Contains(t, completed, "task-a")
	assert.Contains(t, completed, "task-b")
}

func TestReplayWorkflowFailed(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-2", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-2", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-2", TaskID: "task-a", Type: storage.EventTaskStarted, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-2", TaskID: "task-a", Type: storage.EventTaskCompleted, SequenceNum: 4, Timestamp: now},
		{WorkflowID: "wf-2", TaskID: "task-b", Type: storage.EventTaskStarted, SequenceNum: 5, Timestamp: now},
		{WorkflowID: "wf-2", TaskID: "task-b", Type: storage.EventTaskFailed, SequenceNum: 6, Timestamp: now,
			Payload: json.RawMessage(`{"error": "timeout"}`)},
		{WorkflowID: "wf-2", Type: storage.EventWorkflowFailed, SequenceNum: 7, Timestamp: now,
			Payload: json.RawMessage(`{"error": "task-b failed"}`)},
	}

	state, err := Replay(events)
	require.NoError(t, err)

	assert.Equal(t, storage.WorkflowStatusFailed, state.Status)
	assert.Equal(t, "task-b failed", state.ErrorMsg)
	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["task-a"].Status)
	assert.Equal(t, storage.TaskStatusFailed, state.Tasks["task-b"].Status)
	assert.Equal(t, "timeout", state.Tasks["task-b"].ErrorMsg)

	failed := state.FailedTasks()
	assert.Len(t, failed, 1)
	assert.Equal(t, "task-b", failed[0])
}

func TestReplayUntil(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-3", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-3", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-3", TaskID: "task-a", Type: storage.EventTaskStarted, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-3", TaskID: "task-a", Type: storage.EventTaskCompleted, SequenceNum: 4, Timestamp: now},
		{WorkflowID: "wf-3", Type: storage.EventWorkflowCompleted, SequenceNum: 5, Timestamp: now},
	}

	// Replay up to sequence 3: task started but not yet completed.
	state, err := ReplayUntil(events, 3)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusRunning, state.Status)
	assert.Equal(t, storage.TaskStatusRunning, state.Tasks["task-a"].Status)
}

func TestReplayOutOfOrder(t *testing.T) {
	now := time.Now()

	// Events deliberately out of order — Replay should sort by SequenceNum.
	events := []*storage.Event{
		{WorkflowID: "wf-4", Type: storage.EventWorkflowCompleted, SequenceNum: 4, Timestamp: now},
		{WorkflowID: "wf-4", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-4", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-4", TaskID: "t1", Type: storage.EventTaskCompleted, SequenceNum: 3, Timestamp: now},
	}

	state, err := Replay(events)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusCompleted, state.Status)
}

func TestReplayRetry(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-5", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-5", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-5", TaskID: "t1", Type: storage.EventTaskStarted, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-5", TaskID: "t1", Type: storage.EventTaskFailed, SequenceNum: 4, Timestamp: now},
		{WorkflowID: "wf-5", TaskID: "t1", Type: storage.EventTaskRetrying, SequenceNum: 5, Timestamp: now},
		{WorkflowID: "wf-5", TaskID: "t1", Type: storage.EventTaskStarted, SequenceNum: 6, Timestamp: now},
		{WorkflowID: "wf-5", TaskID: "t1", Type: storage.EventTaskCompleted, SequenceNum: 7, Timestamp: now},
		{WorkflowID: "wf-5", Type: storage.EventWorkflowCompleted, SequenceNum: 8, Timestamp: now},
	}

	state, err := Replay(events)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusCompleted, state.Status)
	assert.Equal(t, storage.TaskStatusCompleted, state.Tasks["t1"].Status)
	assert.Equal(t, 2, state.Tasks["t1"].Attempts) // Started twice
}

func TestReplayCompensation(t *testing.T) {
	now := time.Now()

	events := []*storage.Event{
		{WorkflowID: "wf-6", Type: storage.EventWorkflowSubmitted, SequenceNum: 1, Timestamp: now},
		{WorkflowID: "wf-6", Type: storage.EventWorkflowStarted, SequenceNum: 2, Timestamp: now},
		{WorkflowID: "wf-6", TaskID: "t1", Type: storage.EventTaskStarted, SequenceNum: 3, Timestamp: now},
		{WorkflowID: "wf-6", TaskID: "t1", Type: storage.EventTaskCompleted, SequenceNum: 4, Timestamp: now},
		{WorkflowID: "wf-6", TaskID: "t2", Type: storage.EventTaskStarted, SequenceNum: 5, Timestamp: now},
		{WorkflowID: "wf-6", TaskID: "t2", Type: storage.EventTaskFailed, SequenceNum: 6, Timestamp: now},
		{WorkflowID: "wf-6", TaskID: "t1", Type: storage.EventTaskCompensating, SequenceNum: 7, Timestamp: now},
		{WorkflowID: "wf-6", Type: storage.EventWorkflowFailed, SequenceNum: 8, Timestamp: now},
	}

	state, err := Replay(events)
	require.NoError(t, err)
	assert.Equal(t, storage.WorkflowStatusFailed, state.Status)
	assert.Equal(t, storage.TaskStatusCompensating, state.Tasks["t1"].Status)
	assert.Equal(t, storage.TaskStatusFailed, state.Tasks["t2"].Status)
}
