// Package hitlworker implements the HITL Worker for Forge workflows.
// It bridges workflow tasks to the HITL Manager for human approval/input.
package hitlworker

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/hitl"
)

// Worker is the HITL workflow worker.
type Worker struct {
	manager *hitl.Manager
	idGen   func() string // ID generator (injectable for tests)
}

// NewWorker creates a HITL Worker backed by the given HITL Manager.
func NewWorker(manager *hitl.Manager, idGen func() string) *Worker {
	if idGen == nil {
		idGen = defaultIDGen
	}
	return &Worker{
		manager: manager,
		idGen:   idGen,
	}
}

// Execute runs a HITL action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "notify":
		return w.notify(ctx, params)
	case "request_approval":
		return w.requestApproval(ctx, params)
	case "request_input":
		return w.requestInput(ctx, params)
	case "notify_and_wait":
		return w.notifyAndWait(ctx, params)
	default:
		return "", fmt.Errorf("hitl worker: unknown action %q", action)
	}
}

// notify sends a one-way notification (no response expected).
func (w *Worker) notify(ctx context.Context, params map[string]any) (string, error) {
	message, _ := params["message"].(string)
	if message == "" {
		return "", fmt.Errorf("hitl worker: 'message' parameter required")
	}

	workflowID, _ := params["workflow_id"].(string)
	if workflowID == "" {
		return "", fmt.Errorf("hitl worker: 'workflow_id' parameter required")
	}
	taskID, _ := params["task_id"].(string)

	req := &hitl.Request{
		ID:         w.idGen(),
		WorkflowID: workflowID,
		TaskID:     taskID,
		Message:    message,
		Options:    []string{}, // no response expected
	}

	if err := w.manager.Create(ctx, req); err != nil {
		return "", fmt.Errorf("hitl worker: notify: %w", err)
	}

	result := map[string]any{
		"status":     "sent",
		"request_id": req.ID,
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// requestApproval asks for approve/reject decision and waits for response.
func (w *Worker) requestApproval(ctx context.Context, params map[string]any) (string, error) {
	message, _ := params["message"].(string)
	if message == "" {
		return "", fmt.Errorf("hitl worker: 'message' parameter required")
	}

	workflowID, _ := params["workflow_id"].(string)
	if workflowID == "" {
		return "", fmt.Errorf("hitl worker: 'workflow_id' parameter required")
	}
	taskID, _ := params["task_id"].(string)

	options := []string{"approve", "reject"}
	if custom, ok := params["options"].([]any); ok && len(custom) > 0 {
		options = make([]string, len(custom))
		for i, v := range custom {
			options[i] = fmt.Sprintf("%v", v)
		}
	}

	timeout := resolveTimeout(params)

	req := &hitl.Request{
		ID:         w.idGen(),
		WorkflowID: workflowID,
		TaskID:     taskID,
		Message:    message,
		Options:    options,
		TimeoutAt:  time.Now().Add(timeout),
	}

	if err := w.manager.Create(ctx, req); err != nil {
		return "", fmt.Errorf("hitl worker: request_approval: %w", err)
	}

	// Return the request ID — the workflow will be paused until response.
	result := map[string]any{
		"status":     "pending",
		"request_id": req.ID,
		"options":    options,
		"timeout":    timeout.String(),
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// requestInput asks for free-form user input and waits for response.
func (w *Worker) requestInput(ctx context.Context, params map[string]any) (string, error) {
	message, _ := params["message"].(string)
	if message == "" {
		return "", fmt.Errorf("hitl worker: 'message' parameter required")
	}

	workflowID, _ := params["workflow_id"].(string)
	if workflowID == "" {
		return "", fmt.Errorf("hitl worker: 'workflow_id' parameter required")
	}
	taskID, _ := params["task_id"].(string)
	timeout := resolveTimeout(params)

	req := &hitl.Request{
		ID:         w.idGen(),
		WorkflowID: workflowID,
		TaskID:     taskID,
		Message:    message,
		Options:    []string{"respond"}, // free text
		TimeoutAt:  time.Now().Add(timeout),
	}

	if err := w.manager.Create(ctx, req); err != nil {
		return "", fmt.Errorf("hitl worker: request_input: %w", err)
	}

	result := map[string]any{
		"status":     "pending",
		"request_id": req.ID,
		"timeout":    timeout.String(),
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// notifyAndWait sends a notification and waits for acknowledgment.
func (w *Worker) notifyAndWait(ctx context.Context, params map[string]any) (string, error) {
	message, _ := params["message"].(string)
	if message == "" {
		return "", fmt.Errorf("hitl worker: 'message' parameter required")
	}

	workflowID, _ := params["workflow_id"].(string)
	if workflowID == "" {
		return "", fmt.Errorf("hitl worker: 'workflow_id' parameter required")
	}
	taskID, _ := params["task_id"].(string)
	timeout := resolveTimeout(params)

	req := &hitl.Request{
		ID:         w.idGen(),
		WorkflowID: workflowID,
		TaskID:     taskID,
		Message:    message,
		Options:    []string{"ack"},
		TimeoutAt:  time.Now().Add(timeout),
	}

	if err := w.manager.Create(ctx, req); err != nil {
		return "", fmt.Errorf("hitl worker: notify_and_wait: %w", err)
	}

	result := map[string]any{
		"status":     "pending",
		"request_id": req.ID,
		"timeout":    timeout.String(),
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// resolveTimeout extracts timeout from params or returns default.
func resolveTimeout(params map[string]any) time.Duration {
	if v, ok := params["timeout"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if v, ok := params["timeout_minutes"].(float64); ok && v > 0 {
		return time.Duration(v) * time.Minute
	}
	return 24 * time.Hour // default: 24h
}

// defaultIDGen generates a unique ID using timestamp + random suffix.
func defaultIDGen() string {
	return fmt.Sprintf("hitl_%d_%04x", time.Now().UnixNano(), randUint16())
}

// randUint16 returns a pseudo-random uint16 for ID uniqueness.
func randUint16() uint16 {
	var b [2]byte
	// Use crypto/rand for uniqueness; fallback to time-based if it fails
	if _, err := crand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano() & 0xFFFF)
	}
	return uint16(b[0])<<8 | uint16(b[1])
}
