package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/event"
	"github.com/castwell/forge/internal/storage"
)

// The event log is this system's record of what happened, and the task table is
// what every live query reads. They describe the same run, so they must not
// disagree.
//
// They did disagree about compensation: `runCompensation` wrote the
// TASK_COMPENSATING event and never wrote the status, so `forge history`
// reconstructed COMPENSATING while the table and the dashboard still said
// COMPLETED. An auditor reading one and an operator reading the other got
// different answers about the same task.
//
// This is the invariant the fix is for, and it is worth more than the one field
// it repaired: any future path that writes an event without writing the state it
// implies fails here.
func TestTaskTableAgreesWithReplayedEvents(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "agree.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)

	// `first` succeeds but declares a compensation; `second` fails and asks for
	// the rollback. The handler names double as the compensation handlers, so one
	// worker covers both.
	coord.mu.Lock()
	coord.workers["w"] = &WorkerEntry{
		ID: "w", Handlers: []string{"do", "undo"}, Capacity: 10,
		Client: newSelectiveWorker(map[string]string{"second": "expected failure"}),
	}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: `
name: compensating_chain
tasks:
  first:
    handler: do
    compensate: undo
  second:
    handler: do
    depends_on: [first]
    on_failure: COMPENSATE
`,
	})
	require.NoError(t, err)
	workflowID := resp.GetWorkflowId()

	// The run ends FAILED after compensating.
	require.Eventually(t, func() bool {
		wf, err := coord.store.GetWorkflow(context.Background(), workflowID)
		if err != nil {
			return false
		}
		return wf.Status == storage.WorkflowStatusFailed
	}, 20*time.Second, 25*time.Millisecond, "the workflow must fail after compensating")

	// Give the compensation writes a moment to land; they happen inside the
	// compensation goroutine, just before the workflow is marked failed.
	require.Eventually(t, func() bool {
		return taskStatuses(t, coord.store, workflowID)["first"] == storage.TaskStatusCompensating
	}, 10*time.Second, 25*time.Millisecond,
		"the compensated task's status must be recorded, not only its event")

	assertAgreement(t, coord.store, workflowID)
}

// assertAgreement checks that every task's persisted status matches what the
// event log says about it.
func assertAgreement(t *testing.T, store storage.Storage, workflowID string) {
	t.Helper()

	tasks, err := store.ListTasksByWorkflow(context.Background(), workflowID)
	require.NoError(t, err)

	events, err := store.GetWorkflowHistory(context.Background(), workflowID)
	require.NoError(t, err)

	replayed, err := event.Replay(events)
	require.NoError(t, err)

	// The replay keys tasks by id; the table carries both id and name.
	for _, task := range tasks {
		state, ok := replayed.Tasks[task.ID]
		if !ok {
			continue // the event log has nothing to say about it
		}
		assert.Equal(t, task.Status, state.Status,
			"task %q: the table says %s but replaying the events says %s; "+
				"an audit read through one and a live query through the other must not disagree",
			task.TaskName, task.Status, state.Status)
	}
}

// newSelectiveWorker fails for named tasks and succeeds otherwise, so one worker
// can drive both halves of a compensation chain.
func newSelectiveWorker(failures map[string]string) *selectiveWorker {
	return &selectiveWorker{failures: failures}
}

type selectiveWorker struct {
	forgev1.WorkerServiceClient
	failures map[string]string
}

// ExecuteTask fails for the named tasks and succeeds for everything else.
//
// It matches on the task name because that is what the DAG declares; the
// compensation dispatch carries a derived name (`first.compensate`), which is
// deliberately not in the failure map — a rollback that failed would test the
// wrong thing.
func (s *selectiveWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	if msg, shouldFail := s.failures[req.GetTaskName()]; shouldFail {
		return &forgev1.TaskResponse{Success: false, ErrorMsg: msg}, nil
	}
	return &forgev1.TaskResponse{Success: true, Output: []byte(`{"ok":true}`)}, nil
}
