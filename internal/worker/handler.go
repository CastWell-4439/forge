// Package worker implements the Go Worker that connects to a Coordinator
// via gRPC, registers handler functions, and executes dispatched tasks.
package worker

import (
	"context"
	"errors"
	"fmt"
)

// HandlerFunc is the function signature for task handlers.
// It receives context and input parameters, and returns output or an error.
type HandlerFunc func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)

// AwaitingHumanError is returned by a handler that has registered a request for
// human input and cannot proceed until it is answered.
//
// It exists because the handler contract has only one outcome channel — a result
// or an error — and "held for a human" is neither. Reporting it as a plain error
// made a paused task indistinguishable from a failed one; reporting it as a
// success (which is what the HITL worker did) let the workflow run on as though
// approval had been given, while the request sat unanswered.
//
// The Executor translates it into the response's Paused field, which is the
// existing channel for "not a failure, waiting on a person" — the runtime gate
// already uses it. A sentinel rather than a signature change: every handler keeps
// the shape it has, and only the ones that can block need to know about it.
type AwaitingHumanError struct {
	// RequestID identifies the pending request, so the reason string points a
	// reviewer at the thing they are being asked about.
	RequestID string
	// Message is what the human is being asked, short enough for a log line.
	Message string
}

// Error implements error. The text lands in the task's pause reason, so it names
// the request rather than only saying "paused".
func (e *AwaitingHumanError) Error() string {
	if e.RequestID == "" {
		return "awaiting human input"
	}
	return fmt.Sprintf("awaiting human input (request %s)", e.RequestID)
}

// Is lets errors.Is(err, ErrAwaitingHuman) succeed for a typed
// *AwaitingHumanError, so a caller can test one sentinel without unwrapping.
func (e *AwaitingHumanError) Is(target error) bool {
	return target == ErrAwaitingHuman
}

// ErrAwaitingHuman is the sentinel form, for handlers that queue a human request
// but have no specific request ID to report.
var ErrAwaitingHuman = errors.New("awaiting human input")

// Registry maintains a mapping of handler names to handler functions.
type Registry struct {
	handlers map[string]HandlerFunc
}

// NewRegistry creates a new empty handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]HandlerFunc),
	}
}

// Register adds a handler function for the given name.
func (r *Registry) Register(name string, fn HandlerFunc) {
	r.handlers[name] = fn
}

// Get returns the handler function for the given name, or nil if not found.
func (r *Registry) Get(name string) HandlerFunc {
	return r.handlers[name]
}

// Handlers returns all registered handler names.
func (r *Registry) Handlers() []string {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	return names
}
