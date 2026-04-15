// Package worker implements the Go Worker that connects to a Coordinator
// via gRPC, registers handler functions, and executes dispatched tasks.
package worker

import "context"

// HandlerFunc is the function signature for task handlers.
// It receives context and input parameters, and returns output or an error.
type HandlerFunc func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)

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
