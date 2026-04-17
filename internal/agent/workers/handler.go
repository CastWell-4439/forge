// Package workers implements agent tool handlers for video production workflows.
// Each handler corresponds to a Forge Worker handler name (e.g. "ai.face_swap").
// Handlers support two modes: "mock" for testing and "real" for production use.
package workers

import (
	"context"
	"fmt"
)

// HandlerFunc is the function signature for agent tool handlers.
// It receives context and input parameters, and returns output or an error.
type HandlerFunc func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)

// HandlerMode defines the execution mode for handlers.
type HandlerMode string

const (
	// HandlerModeMock returns fake/plausible results without calling real services.
	HandlerModeMock HandlerMode = "mock"
	// HandlerModeReal calls real external services (requires proper configuration).
	HandlerModeReal HandlerMode = "real"
)

// HandlerConfig holds configuration for handler creation.
type HandlerConfig struct {
	Mode      HandlerMode
	Workspace string // Base directory for file operations, e.g. "/tmp/forge"
}

// ErrNotConfigured is returned when a real-mode handler is called but the
// underlying service is not configured.
var ErrNotConfigured = fmt.Errorf("handler not configured: real mode requires external service setup")
