// Package runtime connects Forge runtime events to ForgeX run artifacts.
//
// The package is intentionally observe-only: observer failures must never
// change Forge workflow execution semantics.
package runtime

import (
	"context"
	"encoding/json"
	"time"

	forgestorage "github.com/castwell/forge/internal/storage"
)

// Observer receives Forge runtime events after they have been persisted by the
// Forge coordinator. Implementations should be best-effort and non-blocking in
// spirit: callers log observer errors but do not fail workflow execution.
type Observer interface {
	ObserveEvent(ctx context.Context, event *forgestorage.Event) error
}

// TaskCallObserver is an optional extension for observers that also want the
// Coordinator-side Worker RPC view of a task execution. This is still
// observe-only: it must not alter Worker execution semantics.
type TaskCallObserver interface {
	ObserveTaskCall(ctx context.Context, call TaskCall) error
}

// TaskCall captures the Coordinator-side view of one worker ExecuteTask RPC.
type TaskCall struct {
	WorkflowID string
	TaskID     string
	TaskName   string
	Handler    string
	WorkerID   string
	Input      json.RawMessage
	Output     json.RawMessage
	Error      string
	Success    bool
	StartedAt  time.Time
	EndedAt    time.Time
}

// NoopObserver is the default observer used when ForgeX runtime integration is
// disabled.
type NoopObserver struct{}

// ObserveEvent implements Observer.
func (NoopObserver) ObserveEvent(ctx context.Context, event *forgestorage.Event) error {
	return ctx.Err()
}
