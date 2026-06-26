package trace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
	"github.com/google/uuid"
)

// Recorder records ForgeX runtime events through a storage.Store.
type Recorder struct {
	store storage.Store
	runID string

	mu        sync.Mutex
	toolCalls map[string]model.ToolCall
}

// NewRecorder creates a Recorder for one run.
func NewRecorder(store storage.Store, runID string) *Recorder {
	return &Recorder{
		store:     store,
		runID:     runID,
		toolCalls: make(map[string]model.ToolCall),
	}
}

// Event records a generic event.
func (r *Recorder) Event(ctx context.Context, typ model.EventType, message string, data map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.store.AppendEvent(ctx, model.Event{
		ID:        newID("evt"),
		RunID:     r.runID,
		Type:      typ,
		Message:   message,
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

// ToolCallStarted records a tool call start and returns the generated tool call ID.
func (r *Recorder) ToolCallStarted(ctx context.Context, toolName string, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	call := model.ToolCall{
		ID:        newID("tool"),
		RunID:     r.runID,
		ToolName:  toolName,
		Args:      args,
		StartedAt: time.Now().UTC(),
	}

	r.mu.Lock()
	r.toolCalls[call.ID] = call
	r.mu.Unlock()

	if err := r.store.AppendToolCall(ctx, call); err != nil {
		return "", err
	}
	if err := r.Event(ctx, model.EventToolCalled, fmt.Sprintf("tool called: %s", toolName), map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    toolName,
	}); err != nil {
		return "", err
	}
	return call.ID, nil
}

// ToolCallFinished records a successful tool call completion.
func (r *Recorder) ToolCallFinished(ctx context.Context, callID string, result map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	call, err := r.getToolCall(callID)
	if err != nil {
		return err
	}
	endedAt := time.Now().UTC()
	call.Result = result
	call.EndedAt = endedAt

	if err := r.store.AppendToolCall(ctx, call); err != nil {
		return err
	}
	return r.Event(ctx, model.EventToolSucceeded, fmt.Sprintf("tool succeeded: %s", call.ToolName), map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    call.ToolName,
	})
}

// ToolCallFailed records a failed tool call completion.
func (r *Recorder) ToolCallFailed(ctx context.Context, callID string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err == nil {
		err = fmt.Errorf("unknown tool call error")
	}

	call, getErr := r.getToolCall(callID)
	if getErr != nil {
		return getErr
	}
	endedAt := time.Now().UTC()
	call.Error = err.Error()
	call.EndedAt = endedAt

	if appendErr := r.store.AppendToolCall(ctx, call); appendErr != nil {
		return appendErr
	}
	return r.Event(ctx, model.EventToolFailed, fmt.Sprintf("tool failed: %s", call.ToolName), map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    call.ToolName,
		"error":        err.Error(),
	})
}

// Error records a classified or unclassified error envelope.
func (r *Recorder) Error(ctx context.Context, envelope model.ErrorEnvelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if envelope.RunID == "" {
		envelope.RunID = r.runID
	}
	if envelope.ID == "" {
		envelope.ID = newID("err")
	}
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = time.Now().UTC()
	}
	return r.store.AppendError(ctx, envelope)
}

// StopDecision records a stop-condition decision and emits a stop_decided event.
func (r *Recorder) StopDecision(ctx context.Context, decision model.StopDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if decision.RunID == "" {
		decision.RunID = r.runID
	}
	if decision.ID == "" {
		decision.ID = newID("decision")
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	if err := r.store.AppendStopDecision(ctx, decision); err != nil {
		return err
	}
	return r.Event(ctx, model.EventStopDecided, fmt.Sprintf("stop decision: %s", decision.Action), map[string]any{
		"decision_id": decision.ID,
		"action":      string(decision.Action),
		"reason":      decision.Reason,
	})
}

func (r *Recorder) getToolCall(callID string) (model.ToolCall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	call, ok := r.toolCalls[callID]
	if !ok {
		return model.ToolCall{}, fmt.Errorf("tool call not found: %s", callID)
	}
	return call, nil
}

func newID(prefix string) string {
	return prefix + "_" + uuid.NewString()
}
