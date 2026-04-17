// Package forge provides the public Go Worker SDK for building Forge task workers.
//
// This package is a thin wrapper around the internal worker implementation,
// providing a stable public API for external Go consumers. Internal packages
// may change without notice; this SDK follows semantic versioning.
//
// Usage:
//
//	import forge "github.com/castwell/forge/sdk/go"
//
//	registry := forge.NewRegistry()
//	registry.Register("my.handler", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
//	    return map[string]interface{}{"result": "ok"}, nil
//	})
//
//	w := forge.NewWorker("worker-1", ":9090", "localhost:8080", 10, registry)
//	w.Start(ctx)
package forge

import (
	"context"

	"github.com/castwell/forge/internal/worker"
)

// HandlerFunc is the function signature for task handlers.
// It receives context and input parameters, and returns output or an error.
type HandlerFunc = worker.HandlerFunc

// Registry maintains a mapping of handler names to handler functions.
type Registry = worker.Registry

// NewRegistry creates a new empty handler registry.
func NewRegistry() *Registry {
	return worker.NewRegistry()
}

// Worker connects to a Coordinator, registers itself, and serves task execution requests.
type Worker = worker.Worker

// NewWorker creates a new Worker with the given configuration.
//
// Parameters:
//   - id: unique worker identifier.
//   - addr: address for the worker's gRPC server (e.g., ":9090").
//   - coordAddr: coordinator gRPC address (e.g., "localhost:8080").
//   - capacity: maximum concurrent tasks.
//   - registry: handler registry with registered task handlers.
func NewWorker(id, addr, coordAddr string, capacity int, registry *Registry) *Worker {
	return worker.NewWorker(id, addr, coordAddr, capacity, registry)
}

// Executor executes tasks by looking up handlers in the registry and invoking them.
type Executor = worker.Executor

// NewExecutor creates a new Executor with the given handler registry.
func NewExecutor(registry *Registry) *Executor {
	return worker.NewExecutor(registry)
}

// Start is a convenience function that creates a Worker and starts it.
func Start(ctx context.Context, id, addr, coordAddr string, capacity int, registry *Registry) error {
	w := NewWorker(id, addr, coordAddr, capacity, registry)
	return w.Start(ctx)
}
