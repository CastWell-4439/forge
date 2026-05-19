package hitl

import (
	"context"
	"fmt"
	"time"
)

// WorkflowAPI implements the gRPC WorkflowService handlers.
// This is a pure Go implementation; the actual gRPC binding is done at the server layer.
type WorkflowAPI struct {
	hitlMgr        *Manager
	registryLookup func(name string) (WorkflowInfo, bool)
	listFn         func() []WorkflowInfo
	triggerFn      func(ctx context.Context, workflowName string, inputs map[string]string) (string, error)
	cancelFn       func(ctx context.Context, instanceID string) error
	statusFn       func(ctx context.Context, instanceID string) (*InstanceStatus, error)
}

// WorkflowInfo is a summary of a registered workflow.
type WorkflowInfo struct {
	Name         string
	Version      string
	Description  string
	TriggerTypes []string
}

// InstanceStatus represents the status of a running workflow instance.
type InstanceStatus struct {
	InstanceID   string
	WorkflowName string
	Status       string // running, paused, completed, failed, cancelled
	Tasks        []TaskStatusInfo
	StartedAt    time.Time
	CompletedAt  time.Time
}

// TaskStatusInfo is the status of a single task.
type TaskStatusInfo struct {
	TaskID    string
	StageName string
	Worker    string
	Action    string
	Status    string // pending, running, completed, failed, skipped
	Output    string
	Error     string
}

// WorkflowAPIConfig configures the WorkflowAPI.
type WorkflowAPIConfig struct {
	HITLManager    *Manager
	RegistryLookup func(name string) (WorkflowInfo, bool)
	ListFn         func() []WorkflowInfo
	TriggerFn      func(ctx context.Context, workflowName string, inputs map[string]string) (string, error)
	CancelFn       func(ctx context.Context, instanceID string) error
	StatusFn       func(ctx context.Context, instanceID string) (*InstanceStatus, error)
}

// NewWorkflowAPI creates a new WorkflowAPI.
func NewWorkflowAPI(cfg WorkflowAPIConfig) *WorkflowAPI {
	return &WorkflowAPI{
		hitlMgr:        cfg.HITLManager,
		registryLookup: cfg.RegistryLookup,
		listFn:         cfg.ListFn,
		triggerFn:      cfg.TriggerFn,
		cancelFn:       cfg.CancelFn,
		statusFn:       cfg.StatusFn,
	}
}

// TriggerWorkflow manually triggers a workflow.
func (a *WorkflowAPI) TriggerWorkflow(ctx context.Context, workflowName string, inputs map[string]string) (string, error) {
	if a.triggerFn == nil {
		return "", fmt.Errorf("workflow api: trigger not configured")
	}
	return a.triggerFn(ctx, workflowName, inputs)
}

// GetWorkflowStatus returns the status of a workflow instance.
func (a *WorkflowAPI) GetWorkflowStatus(ctx context.Context, instanceID string) (*InstanceStatus, error) {
	if a.statusFn == nil {
		return nil, fmt.Errorf("workflow api: status not configured")
	}
	return a.statusFn(ctx, instanceID)
}

// ResumeHITL responds to a HITL request.
func (a *WorkflowAPI) ResumeHITL(ctx context.Context, requestID, decision, feedback string) error {
	resp := &Response{
		Decision: decision,
		Feedback: feedback,
	}
	return a.hitlMgr.Respond(ctx, requestID, resp)
}

// ListWorkflows returns all registered workflows.
func (a *WorkflowAPI) ListWorkflows() []WorkflowInfo {
	if a.registryLookup == nil {
		return nil
	}
	if a.listFn != nil {
		return a.listFn()
	}
	return nil
}

// PauseWorkflow pauses a running workflow instance (creates a HITL request).
func (a *WorkflowAPI) PauseWorkflow(ctx context.Context, instanceID, reason string) error {
	req := &Request{
		ID:         "pause-" + instanceID,
		WorkflowID: instanceID,
		TaskID:     "_manual_pause",
		Message:    reason,
		Options:    []string{"resume", "cancel"},
	}
	return a.hitlMgr.Create(ctx, req)
}

// CancelWorkflow cancels a running workflow instance.
func (a *WorkflowAPI) CancelWorkflow(ctx context.Context, instanceID string) error {
	if a.cancelFn == nil {
		return fmt.Errorf("workflow api: cancel not configured")
	}
	return a.cancelFn(ctx, instanceID)
}
