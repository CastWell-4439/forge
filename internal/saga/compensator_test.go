package saga

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStorage implements just enough of storage.Storage for saga tests.
type mockStorage struct {
	tasks []*storage.Task
}

func (m *mockStorage) ListTasksByWorkflow(_ context.Context, _ string) ([]*storage.Task, error) {
	return m.tasks, nil
}

// Unused methods — satisfy interface.
func (m *mockStorage) SaveWorkflowDefinition(_ context.Context, _ *storage.WorkflowDefinition) error { return nil }
func (m *mockStorage) GetWorkflowDefinition(_ context.Context, _ string, _ int) (*storage.WorkflowDefinition, error) { return nil, nil }
func (m *mockStorage) SaveWorkflow(_ context.Context, _ *storage.Workflow) error { return nil }
func (m *mockStorage) GetWorkflow(_ context.Context, _ string) (*storage.Workflow, error) { return nil, nil }
func (m *mockStorage) ListWorkflows(_ context.Context, _ storage.WorkflowStatus, _ int, _ int) ([]*storage.Workflow, error) { return nil, nil }
func (m *mockStorage) UpdateWorkflowStatus(_ context.Context, _ string, _ storage.WorkflowStatus) error { return nil }
func (m *mockStorage) SaveTask(_ context.Context, _ *storage.Task) error { return nil }
func (m *mockStorage) GetTask(_ context.Context, _ string) (*storage.Task, error) { return nil, nil }
func (m *mockStorage) ClaimTask(_ context.Context, _ string, _ []string) (*storage.Task, error) { return nil, nil }
func (m *mockStorage) UpdateTaskStatus(_ context.Context, _ string, _ storage.TaskStatus) error { return nil }
func (m *mockStorage) CompleteTask(_ context.Context, _ string, _ json.RawMessage) error { return nil }
func (m *mockStorage) FailTask(_ context.Context, _ string, _ string) error { return nil }
func (m *mockStorage) SaveEvent(_ context.Context, _ *storage.Event) error { return nil }
func (m *mockStorage) GetWorkflowHistory(_ context.Context, _ string) ([]*storage.Event, error) { return nil, nil }
func (m *mockStorage) Close() error { return nil }

func TestBuildCompensationPlan(t *testing.T) {
	// DAG: A → B → C, where C fails.
	// A and B have compensate handlers, C does not.
	dag := buildTestDAG()

	store := &mockStorage{
		tasks: []*storage.Task{
			{ID: "id-a", TaskName: "create-order", WorkflowID: "wf-1", Status: storage.TaskStatusCompleted, Input: json.RawMessage(`{"order_id": 123}`)},
			{ID: "id-b", TaskName: "charge-payment", WorkflowID: "wf-1", Status: storage.TaskStatusCompleted, Input: json.RawMessage(`{"amount": 100}`)},
			{ID: "id-c", TaskName: "send-notification", WorkflowID: "wf-1", Status: storage.TaskStatusFailed},
		},
	}

	comp := NewCompensator(store)
	plan, err := comp.BuildPlan(context.Background(), dag, "wf-1", "send-notification")
	require.NoError(t, err)

	assert.Equal(t, "wf-1", plan.WorkflowID)
	assert.Equal(t, "send-notification", plan.FailedTask)

	// Should have 2 steps: charge-payment (reverse topo first) then create-order.
	require.Len(t, plan.Steps, 2)
	assert.Equal(t, "charge-payment", plan.Steps[0].TaskName)
	assert.Equal(t, "payment.refund", plan.Steps[0].CompensateHandler)
	assert.Equal(t, "create-order", plan.Steps[1].TaskName)
	assert.Equal(t, "order.cancel", plan.Steps[1].CompensateHandler)
}

func TestExecuteCompensationAllSuccess(t *testing.T) {
	plan := &CompensationPlan{
		WorkflowID: "wf-1",
		FailedTask: "task-c",
		Steps: []CompensationStep{
			{TaskName: "task-b", CompensateHandler: "b.compensate"},
			{TaskName: "task-a", CompensateHandler: "a.compensate"},
		},
	}

	var executionOrder []string
	results := Execute(plan, func(step CompensationStep) error {
		executionOrder = append(executionOrder, step.TaskName)
		return nil
	})

	assert.Len(t, results, 2)
	assert.True(t, AllSucceeded(results))
	// Verify execution order is B then A (reverse topo).
	assert.Equal(t, []string{"task-b", "task-a"}, executionOrder)
}

func TestExecuteCompensationPartialFailure(t *testing.T) {
	plan := &CompensationPlan{
		WorkflowID: "wf-1",
		FailedTask: "task-c",
		Steps: []CompensationStep{
			{TaskName: "task-b", CompensateHandler: "b.compensate"},
			{TaskName: "task-a", CompensateHandler: "a.compensate"},
		},
	}

	results := Execute(plan, func(step CompensationStep) error {
		if step.TaskName == "task-b" {
			return fmt.Errorf("compensation failed")
		}
		return nil
	})

	assert.Len(t, results, 2)
	assert.False(t, AllSucceeded(results))
	assert.False(t, results[0].Success)
	assert.Equal(t, "compensation failed", results[0].ErrorMsg)
	assert.True(t, results[1].Success) // task-a compensation still runs
}

func TestBuildPlanSkipsIncompleteAndNoCompensate(t *testing.T) {
	dag := buildTestDAG()

	// Only create-order is completed, charge-payment is still running.
	store := &mockStorage{
		tasks: []*storage.Task{
			{ID: "id-a", TaskName: "create-order", WorkflowID: "wf-2", Status: storage.TaskStatusCompleted},
			{ID: "id-b", TaskName: "charge-payment", WorkflowID: "wf-2", Status: storage.TaskStatusRunning},
			{ID: "id-c", TaskName: "send-notification", WorkflowID: "wf-2", Status: storage.TaskStatusFailed},
		},
	}

	comp := NewCompensator(store)
	plan, err := comp.BuildPlan(context.Background(), dag, "wf-2", "send-notification")
	require.NoError(t, err)

	// Only create-order should be in the plan (charge-payment not completed).
	require.Len(t, plan.Steps, 1)
	assert.Equal(t, "create-order", plan.Steps[0].TaskName)
}

func TestExecuteEmptyPlan(t *testing.T) {
	plan := &CompensationPlan{
		WorkflowID: "wf-3",
		FailedTask: "task-a",
		Steps:      nil,
	}

	results := Execute(plan, func(step CompensationStep) error {
		t.Fatal("should not be called")
		return nil
	})

	assert.Empty(t, results)
	assert.True(t, AllSucceeded(results))
}

// buildTestDAG creates a test DAG: create-order → charge-payment → send-notification.
func buildTestDAG() *coordinator.DAG {
	dagYAML := `
name: order-flow
tasks:
  create-order:
    handler: order.create
    compensate: order.cancel
  charge-payment:
    handler: payment.charge
    depends_on: [create-order]
    compensate: payment.refund
  send-notification:
    handler: notify.send
    depends_on: [charge-payment]
    on_failure: COMPENSATE
`
	dag, _ := coordinator.ParseDAG([]byte(dagYAML))
	return dag
}
