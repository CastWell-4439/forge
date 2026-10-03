package wasm

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wasmruntime "github.com/castwell/forge/internal/wasm"
)

// loadEcho reads the committed echo.wasm fixture — the same module the
// runtime tests execute, so this test proves the worker path uses the REAL
// wazero runtime end to end, not the stub.
func loadEcho(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../wasm/testdata/echo.wasm")
	require.NoError(t, err, "echo.wasm fixture missing")
	return data
}

func newEchoWorker(t *testing.T) *Worker {
	t.Helper()
	registry := wasmruntime.NewRegistry()
	require.NoError(t, registry.Register("echo", "1.0.0", "stdin/stdout echo plugin", loadEcho(t)))
	return NewWorker(registry, 64)
}

// The dispatch chain runs a real plugin: action = plugin name, params flow
// into TaskInput, the plugin's stdout comes back as the task's JSON output.
func TestExecuteRunsPluginInRealSandbox(t *testing.T) {
	w := newEchoWorker(t)

	out, err := w.Execute(context.Background(), "echo", map[string]any{
		"msg": "hello-wasm",
	})
	require.NoError(t, err)

	var parsed wasmruntime.TaskOutput
	require.NoError(t, json.Unmarshal([]byte(out), &parsed), "worker returns TaskOutput JSON")
	assert.Contains(t, parsed.Result, "hello-wasm", "the param reached the plugin through stdin")
	assert.Contains(t, parsed.Result, "echo", "TaskInput metadata carries the plugin name")
}

// A missing plugin must name what IS loaded instead of a bare not-found.
func TestExecuteUnknownPluginListsLoaded(t *testing.T) {
	w := newEchoWorker(t)

	_, err := w.Execute(context.Background(), "nope", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loaded:")
	assert.Contains(t, err.Error(), "echo")
}

// Action is the plugin name; without it the worker says so (and shows the
// list) instead of failing obscurely.
func TestExecuteRequiresAction(t *testing.T) {
	w := newEchoWorker(t)

	_, err := w.Execute(context.Background(), "  ", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin name")
}

// Structured params arrive as JSON strings, not Go's map printing.
func TestStringifyParamsEncodesStructures(t *testing.T) {
	out := stringifyParams(map[string]any{
		"n":     42,
		"obj":   map[string]any{"k": "v"},
		"plain": "text",
	})
	assert.Equal(t, "42", out["n"])
	assert.Equal(t, `{"k":"v"}`, out["obj"])
	assert.Equal(t, "text", out["plain"])
}
