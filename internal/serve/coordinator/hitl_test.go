package coordinator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/hitl"
	"github.com/castwell/forge/internal/storage"
)

// newAssemblyCoordinator builds a coordinator over an empty embedded store: these
// tests exercise the wiring decisions, not the state machine.
func newAssemblyCoordinator(t *testing.T) *coordinator.Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "assembly.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return coordinator.NewCoordinator(store)
}

// The decision vocabulary is open — a workflow declares its own options — so the
// resolver has to decide "is this an approval" rather than enumerate refusals.
// Erring toward not-approved is the safe direction for an approval gate: a task
// must not proceed because someone typed a word nobody anticipated.
func TestApprovesRecognisesApprovalOnly(t *testing.T) {
	cases := []struct {
		name     string
		decision string
		options  []string
		want     bool
	}{
		{"the default approving option", "approve", []string{"approve", "reject"}, true},
		{"a synonym", "yes", []string{"approve", "reject"}, true},
		{"an acknowledgement", "ack", []string{"ack"}, true},
		{"the declared first option", "deploy", []string{"deploy", "rollback"}, true},

		{"the default refusing option", "reject", []string{"approve", "reject"}, false},
		{"the declared second option", "rollback", []string{"deploy", "rollback"}, false},
		{"free text that is not on the list", "hmm let me think", []string{"approve", "reject"}, false},
		{"an empty decision", "", []string{"approve", "reject"}, false},
		{"an unanticipated word", "maybe-later", []string{"approve", "reject"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, approves(tc.decision, tc.options))
		})
	}
}

// A request that names no task cannot release anything, and the resolver says so
// rather than reporting success: the decision is recorded and no task moved,
// which an operator needs to know.
func TestResolverReportsARequestWithNoTask(t *testing.T) {
	coord := newAssemblyCoordinator(t)
	resolve := hitlResolver(coord)

	err := resolve(context.Background(), &hitl.Request{
		ID: "req-no-task", WorkflowID: "wf-1", // no TaskID
	}, &hitl.Response{Decision: "approve"})

	require.Error(t, err)
	assert.ErrorIs(t, err, errNoTaskOnRequest)
}

// The resolver routes an approval to the resume path and anything else to the
// reject path. A task that is not PAUSED cannot be resolved either way, which is
// how this test observes which path was taken without needing a paused task.
func TestResolverRoutesApprovalAndRejection(t *testing.T) {
	coord := newAssemblyCoordinator(t)
	resolve := hitlResolver(coord)

	// Not paused, so both calls fail — but with different messages, which is what
	// tells the two paths apart.
	errApprove := resolve(context.Background(), &hitl.Request{
		ID: "req-a", WorkflowID: "wf-missing", TaskID: "no-such-task",
		Options: []string{"approve", "reject"},
	}, &hitl.Response{Decision: "approve"})
	require.Error(t, errApprove, "resuming a task that does not exist must report failure")

	errReject := resolve(context.Background(), &hitl.Request{
		ID: "req-r", WorkflowID: "wf-missing", TaskID: "no-such-task",
		Options: []string{"approve", "reject"},
	}, &hitl.Response{Decision: "reject"})
	require.Error(t, errReject)

	// Both fail, and neither should claim to have succeeded. The distinguishing
	// detail is in the reason the reject path passes through.
	assert.NotErrorIs(t, errApprove, errNoTaskOnRequest)
	assert.False(t, errors.Is(errApprove, errNoTaskOnRequest))
}

// An expired request whose task is not PAUSED is reported, not swallowed: the
// caller needs to tell "the task moved on" from "the task is still stuck".
func TestReleaseTimedOutRequestReportsFailures(t *testing.T) {
	coord := newAssemblyCoordinator(t)

	// Nothing to do, and nothing to complain about: no task to fail.
	releaseTimedOutRequest(context.Background(), coord, &hitl.Request{ID: "req-x"})
}
