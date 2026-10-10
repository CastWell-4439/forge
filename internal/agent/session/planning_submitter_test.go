package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// The planner compares workflow status against bare names, and this adapter is
// what produces them. A mismatch here would not fail loudly — the planner would
// simply never see a terminal status and wait until its deadline — so the mapping
// is pinned rather than assumed.
func TestWorkflowStatusNamesMatchWhatThePlannerCompares(t *testing.T) {
	cases := map[forgev1.WorkflowStatus]string{
		forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED: "COMPLETED",
		forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED:    "FAILED",
		forgev1.WorkflowStatus_WORKFLOW_STATUS_CANCELLED: "CANCELLED",
		forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING:   "RUNNING",
		forgev1.WorkflowStatus_WORKFLOW_STATUS_PENDING:   "PENDING",
		forgev1.WorkflowStatus_WORKFLOW_STATUS_PAUSED:    "PAUSED",
	}
	for status, want := range cases {
		t.Run(want, func(t *testing.T) {
			assert.Equal(t, want, workflowStatusName(status))
		})
	}
}

func TestTaskStatusNames(t *testing.T) {
	cases := map[forgev1.TaskStatus]string{
		forgev1.TaskStatus_TASK_STATUS_COMPLETED: "COMPLETED",
		forgev1.TaskStatus_TASK_STATUS_FAILED:    "FAILED",
		forgev1.TaskStatus_TASK_STATUS_SKIPPED:   "SKIPPED",
		forgev1.TaskStatus_TASK_STATUS_RUNNING:   "RUNNING",
	}
	for status, want := range cases {
		t.Run(want, func(t *testing.T) {
			assert.Equal(t, want, taskStatusName(status))
		})
	}
}

// An unexpected value is returned as itself rather than as an empty string: a
// blank status compares equal to nothing and would be invisible in a log.
func TestTrimEnumPrefixLeavesUnknownNamesAlone(t *testing.T) {
	assert.Equal(t, "SOMETHING_ELSE", trimEnumPrefix("SOMETHING_ELSE", "WORKFLOW_STATUS_"))
	assert.Equal(t, "WORKFLOW_STATUS_", trimEnumPrefix("WORKFLOW_STATUS_", "WORKFLOW_STATUS_"),
		"a name that is only the prefix is not trimmed into nothing")
	assert.Equal(t, "", trimEnumPrefix("", "X_"))
}

// toSnapshot carries the fields the planner reads, including each task's output —
// that is what acceptance checks are matched against.
func TestToSnapshotCarriesTaskOutputs(t *testing.T) {
	inst := &forgev1.WorkflowInstance{
		Id:       "wf-1",
		Name:     "plan",
		Status:   forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED,
		ErrorMsg: "",
		Tasks: []*forgev1.TaskInstance{
			{
				TaskName: "gather",
				Handler:  "agent",
				Status:   forgev1.TaskStatus_TASK_STATUS_COMPLETED,
				Output:   []byte(`{"result":"collected 3 sources"}`),
			},
			{
				TaskName: "produce",
				Handler:  "agent",
				Status:   forgev1.TaskStatus_TASK_STATUS_FAILED,
				ErrorMsg: "ran out of budget",
			},
		},
	}

	snap := toSnapshot(inst)

	assert.Equal(t, "wf-1", snap.ID)
	assert.Equal(t, "COMPLETED", snap.Status)
	assert.True(t, snap.IsTerminal())
	assert.True(t, snap.Succeeded())

	require.Len(t, snap.Tasks, 2)
	assert.Equal(t, "gather", snap.Tasks[0].Name)
	assert.JSONEq(t, `{"result":"collected 3 sources"}`, string(snap.Tasks[0].Output))
	assert.Equal(t, "ran out of budget", snap.Tasks[1].ErrorMsg)
}

// A nil instance is reported as unknown rather than panicking: a missing workflow
// is a state to describe, not a crash.
func TestToSnapshotHandlesNil(t *testing.T) {
	snap := toSnapshot(nil)
	require.NotNil(t, snap)
	assert.Equal(t, "UNKNOWN", snap.Status)
	assert.False(t, snap.IsTerminal())
	assert.False(t, snap.Succeeded())
}

// A malformed task output is carried through as-is. The planner treats outputs as
// text to search, so quoting them back into valid JSON would lose information
// rather than fix anything.
func TestToSnapshotKeepsRawOutput(t *testing.T) {
	inst := &forgev1.WorkflowInstance{
		Status: forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING,
		Tasks: []*forgev1.TaskInstance{
			{TaskName: "t", Output: []byte(`not json at all`)},
		},
	}
	snap := toSnapshot(inst)
	require.Len(t, snap.Tasks, 1)
	assert.Equal(t, json.RawMessage("not json at all"), snap.Tasks[0].Output)
}

// The adapter satisfies the interface the planner declares. This is a
// compile-time fact, restated as a test so a signature change shows up as a
// failing test rather than as a build error in an unrelated package.
func TestForgeClientImplementsSubmitter(t *testing.T) {
	client := NewForgeClientFromService(nil)
	require.NotNil(t, client)

	submitter := client.AsSubmitter()
	require.NotNil(t, submitter)
}
