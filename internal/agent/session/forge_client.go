package session

import (
	"context"
	"fmt"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"google.golang.org/grpc"
)

// ForgeClient wraps the Coordinator gRPC client for agent use.
type ForgeClient struct {
	client forgev1.CoordinatorServiceClient
}

// NewForgeClient creates a new ForgeClient from a gRPC connection.
func NewForgeClient(conn grpc.ClientConnInterface) *ForgeClient {
	return &ForgeClient{
		client: forgev1.NewCoordinatorServiceClient(conn),
	}
}

// NewForgeClientFromService creates a new ForgeClient from an existing
// CoordinatorServiceClient (useful for testing with mocks).
func NewForgeClientFromService(client forgev1.CoordinatorServiceClient) *ForgeClient {
	return &ForgeClient{client: client}
}

// Submit submits a DAG YAML to Forge for execution and returns the workflow ID.
func (c *ForgeClient) Submit(ctx context.Context, dagYAML string) (string, error) {
	resp, err := c.client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	if err != nil {
		return "", fmt.Errorf("submit workflow: %w", err)
	}
	return resp.GetWorkflowId(), nil
}

// Watch retrieves the current state of a workflow by ID.
func (c *ForgeClient) Watch(ctx context.Context, workflowID string) (*forgev1.WorkflowInstance, error) {
	resp, err := c.client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
		WorkflowId: workflowID,
	})
	if err != nil {
		return nil, fmt.Errorf("watch workflow %s: %w", workflowID, err)
	}
	return resp.GetWorkflow(), nil
}

// Cancel cancels a running workflow.
func (c *ForgeClient) Cancel(ctx context.Context, workflowID string) error {
	_, err := c.client.CancelWorkflow(ctx, &forgev1.CancelWorkflowRequest{
		WorkflowId: workflowID,
		Reason:     "cancelled by agent",
	})
	if err != nil {
		return fmt.Errorf("cancel workflow %s: %w", workflowID, err)
	}
	return nil
}

// Get retrieves a workflow instance by ID.
func (c *ForgeClient) Get(ctx context.Context, workflowID string) (*forgev1.WorkflowInstance, error) {
	resp, err := c.client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
		WorkflowId: workflowID,
	})
	if err != nil {
		return nil, fmt.Errorf("get workflow %s: %w", workflowID, err)
	}
	return resp.GetWorkflow(), nil
}
