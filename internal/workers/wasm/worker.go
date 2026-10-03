// Package wasm implements the Wasm plugin workflow worker: a workflow task
// whose action names a plugin loaded from the plugins directory, executed in
// the wazero sandbox. It is the dispatch half of the plugin system —
// internal/wasm provides the runtime, registry and sandbox; this worker puts
// them on the dispatch chain.
package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	wasmruntime "github.com/castwell/forge/internal/wasm"
)

// Worker executes named Wasm plugins as workflow tasks.
type Worker struct {
	plugins *wasmruntime.Registry
	exec    *wasmruntime.Executor
}

// NewWorker creates the worker with its own wazero runtime and sandboxed
// executor. The executor is built with the REAL wazero runtime — the
// stub that NewExecutor falls back on for nil is a test seam, never this
// path.
func NewWorker(plugins *wasmruntime.Registry, memoryLimitMB uint32) *Worker {
	rt := wasmruntime.NewWazeroRuntime(memoryLimitMB)
	return &Worker{
		plugins: plugins,
		exec:    wasmruntime.NewExecutor(wasmruntime.DefaultSandboxConfig(), rt.ExecuteFunc()),
	}
}

// Execute runs the plugin named by the action: action is the plugin name
// (workflow YAML: worker: wasm, action: echo), params become the plugin's
// TaskInput. The result is the plugin's TaskOutput as JSON, the same
// string-out contract every other workflow worker speaks.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	if strings.TrimSpace(action) == "" {
		return "", fmt.Errorf("wasm worker: action is the plugin name and is required (loaded: %s)", w.loadedNames())
	}

	module, err := w.plugins.Get(action)
	if err != nil {
		return "", fmt.Errorf("wasm worker: %w (loaded: %s)", err, w.loadedNames())
	}

	out, err := w.exec.Execute(ctx, module, wasmruntime.TaskInput{
		Params:   stringifyParams(params),
		Metadata: action,
	})
	if err != nil {
		return "", fmt.Errorf("wasm worker: plugin %q: %w", action, err)
	}

	body, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("wasm worker: marshal result of %q: %w", action, err)
	}
	return string(body), nil
}

// loadedNames lists the registered plugins for error messages: a missing
// plugin must say what IS there instead of a bare not-found.
func (w *Worker) loadedNames() string {
	plugins := w.plugins.List()
	if len(plugins) == 0 {
		return "<none>"
	}
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, fmt.Sprintf("%s@%s", p.Name, p.Active))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// stringifyParams converts workflow params to the flat string map the
// TaskInput protocol carries. Plain strings pass through unwrapped (sql,
// command and friends arrive as themselves); structured and numeric values
// become their JSON encoding rather than Go's map printing.
func stringifyParams(params map[string]any) map[string]string {
	out := make(map[string]string, len(params))
	for key, value := range params {
		if s, ok := value.(string); ok {
			out[key] = s
			continue
		}
		if encoded, err := json.Marshal(value); err == nil {
			out[key] = string(encoded)
		} else {
			out[key] = fmt.Sprintf("%v", value)
		}
	}
	return out
}
