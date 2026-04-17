package agent

import (
	"context"
	"fmt"
	"testing"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// --- Session state machine tests ---

func TestNewSession(t *testing.T) {
	s := NewSession()
	assert.NotEmpty(t, s.ID)
	assert.Equal(t, StateIdle, s.State)
	assert.Empty(t, s.Messages)
	assert.Nil(t, s.Requirement)
	assert.Equal(t, 0, s.RetryCount)
	assert.Empty(t, s.WorkflowID)
	assert.False(t, s.CreatedAt.IsZero())
}

func TestSessionTransitionHappyPath(t *testing.T) {
	s := NewSession()

	transitions := []SessionState{
		StateParsing,
		StatePlanning,
		StateExecuting,
		StateChecking,
		StateCompleted,
	}

	for _, target := range transitions {
		err := s.Transition(target)
		require.NoError(t, err, "transition to %s failed", target)
		assert.Equal(t, target, s.GetState())
	}
}

func TestSessionTransitionWithFix(t *testing.T) {
	s := NewSession()

	// Happy path through fix cycle.
	require.NoError(t, s.Transition(StateParsing))
	require.NoError(t, s.Transition(StatePlanning))
	require.NoError(t, s.Transition(StateExecuting))
	require.NoError(t, s.Transition(StateChecking))
	require.NoError(t, s.Transition(StateFixing))
	require.NoError(t, s.Transition(StateExecuting))
	require.NoError(t, s.Transition(StateChecking))
	require.NoError(t, s.Transition(StateCompleted))

	assert.Equal(t, StateCompleted, s.GetState())
}

func TestSessionTransitionInvalid(t *testing.T) {
	tests := []struct {
		name   string
		from   SessionState
		to     SessionState
	}{
		{"idle to executing", StateIdle, StateExecuting},
		{"idle to completed", StateIdle, StateCompleted},
		{"parsing to executing", StateParsing, StateExecuting},
		{"executing to completed", StateExecuting, StateCompleted},
		{"completed to idle", StateCompleted, StateIdle},
		{"failed to idle", StateFailed, StateIdle},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{ID: "test", State: tc.from}
			err := s.Transition(tc.to)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid transition")
		})
	}
}

func TestSessionTransitionToFailed(t *testing.T) {
	// Every non-terminal state can transition to failed.
	states := []SessionState{
		StateIdle, StateParsing, StatePlanning,
		StateExecuting, StateChecking, StateFixing,
	}

	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			s := &Session{ID: "test", State: state}
			err := s.Transition(StateFailed)
			assert.NoError(t, err)
			assert.Equal(t, StateFailed, s.GetState())
		})
	}
}

func TestSessionAddMessage(t *testing.T) {
	s := NewSession()
	s.AddMessage(Message{Role: "user", Content: "hello"})
	s.AddMessage(Message{Role: "assistant", Content: "hi"})
	assert.Len(t, s.Messages, 2)
	assert.Equal(t, "user", s.Messages[0].Role)
}

func TestSessionCanRetry(t *testing.T) {
	s := NewSession()
	assert.True(t, s.CanRetry())

	s.IncrementRetry()
	s.IncrementRetry()
	assert.True(t, s.CanRetry())

	s.IncrementRetry()
	assert.False(t, s.CanRetry())
}

func TestSessionSetters(t *testing.T) {
	s := NewSession()

	s.SetWorkflowID("wf-123")
	assert.Equal(t, "wf-123", s.WorkflowID)

	req := &VideoRequirement{Description: "test"}
	s.SetRequirement(req)
	assert.Equal(t, "test", s.Requirement.Description)
}

// --- InMemorySessionStore tests ---

func TestInMemorySessionStoreCRUD(t *testing.T) {
	store := NewInMemorySessionStore()

	// Save.
	s := NewSession()
	require.NoError(t, store.Save(s))

	// Get.
	got, err := store.Get(s.ID)
	require.NoError(t, err)
	assert.Equal(t, s.ID, got.ID)

	// List.
	list, err := store.List()
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// Delete.
	require.NoError(t, store.Delete(s.ID))
	_, err = store.Get(s.ID)
	assert.Error(t, err)
}

func TestInMemorySessionStoreGetNotFound(t *testing.T) {
	store := NewInMemorySessionStore()
	_, err := store.Get("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestInMemorySessionStoreDeleteNotFound(t *testing.T) {
	store := NewInMemorySessionStore()
	err := store.Delete("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- ForgeClient tests (with mock gRPC) ---

// mockCoordinatorClient implements forgev1.CoordinatorServiceClient for testing.
type mockCoordinatorClient struct {
	submitResp  *forgev1.SubmitWorkflowResponse
	getResp     *forgev1.GetWorkflowResponse
	cancelResp  *forgev1.CancelWorkflowResponse
	submitErr   error
	getErr      error
	cancelErr   error
}

func (m *mockCoordinatorClient) SubmitWorkflow(_ context.Context, _ *forgev1.SubmitWorkflowRequest, _ ...grpc.CallOption) (*forgev1.SubmitWorkflowResponse, error) {
	return m.submitResp, m.submitErr
}

func (m *mockCoordinatorClient) GetWorkflow(_ context.Context, _ *forgev1.GetWorkflowRequest, _ ...grpc.CallOption) (*forgev1.GetWorkflowResponse, error) {
	return m.getResp, m.getErr
}

func (m *mockCoordinatorClient) ListWorkflows(_ context.Context, _ *forgev1.ListWorkflowsRequest, _ ...grpc.CallOption) (*forgev1.ListWorkflowsResponse, error) {
	return nil, nil
}

func (m *mockCoordinatorClient) CancelWorkflow(_ context.Context, _ *forgev1.CancelWorkflowRequest, _ ...grpc.CallOption) (*forgev1.CancelWorkflowResponse, error) {
	return m.cancelResp, m.cancelErr
}

func TestForgeClientSubmit(t *testing.T) {
	mock := &mockCoordinatorClient{
		submitResp: &forgev1.SubmitWorkflowResponse{WorkflowId: "wf-abc-123"},
	}
	client := NewForgeClientFromService(mock)

	id, err := client.Submit(context.Background(), "name: test\ntasks: {}")
	require.NoError(t, err)
	assert.Equal(t, "wf-abc-123", id)
}

func TestForgeClientSubmitError(t *testing.T) {
	mock := &mockCoordinatorClient{
		submitErr: fmt.Errorf("connection refused"),
	}
	client := NewForgeClientFromService(mock)

	_, err := client.Submit(context.Background(), "name: test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "submit workflow")
}

func TestForgeClientWatch(t *testing.T) {
	mock := &mockCoordinatorClient{
		getResp: &forgev1.GetWorkflowResponse{
			Workflow: &forgev1.WorkflowInstance{
				Id:     "wf-123",
				Status: forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING,
			},
		},
	}
	client := NewForgeClientFromService(mock)

	wf, err := client.Watch(context.Background(), "wf-123")
	require.NoError(t, err)
	assert.Equal(t, "wf-123", wf.GetId())
	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_RUNNING, wf.GetStatus())
}

func TestForgeClientCancel(t *testing.T) {
	mock := &mockCoordinatorClient{
		cancelResp: &forgev1.CancelWorkflowResponse{},
	}
	client := NewForgeClientFromService(mock)

	err := client.Cancel(context.Background(), "wf-123")
	assert.NoError(t, err)
}

func TestForgeClientGet(t *testing.T) {
	mock := &mockCoordinatorClient{
		getResp: &forgev1.GetWorkflowResponse{
			Workflow: &forgev1.WorkflowInstance{
				Id:     "wf-456",
				Name:   "test-workflow",
				Status: forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED,
			},
		},
	}
	client := NewForgeClientFromService(mock)

	wf, err := client.Get(context.Background(), "wf-456")
	require.NoError(t, err)
	assert.Equal(t, "wf-456", wf.GetId())
	assert.Equal(t, "test-workflow", wf.GetName())
	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED, wf.GetStatus())
}

func TestForgeClientGetError(t *testing.T) {
	mock := &mockCoordinatorClient{
		getErr: fmt.Errorf("not found"),
	}
	client := NewForgeClientFromService(mock)

	_, err := client.Get(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "get workflow")
}
