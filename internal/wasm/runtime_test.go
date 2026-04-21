package wasm

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadEchoWasm loads the pre-compiled echo.wasm test fixture.
func loadEchoWasm(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/echo.wasm")
	require.NoError(t, err, "echo.wasm not found — run: GOOS=wasip1 GOARCH=wasm go build -o internal/wasm/testdata/echo.wasm ./cmd/echo-plugin")
	return data
}

func TestWazeroRuntime_Execute_Echo(t *testing.T) {
	wasmBytes := loadEchoWasm(t)
	rt := NewWazeroRuntime(64)

	input := TaskInput{Params: map[string]string{"greeting": "hello"}}
	inputJSON, err := json.Marshal(input)
	require.NoError(t, err)

	out, err := rt.Execute(context.Background(), wasmBytes, inputJSON)
	require.NoError(t, err)

	var output TaskOutput
	require.NoError(t, json.Unmarshal(out, &output))
	assert.Contains(t, output.Result, "greeting")
	assert.Contains(t, output.Result, "hello")
}

func TestWazeroRuntime_Execute_EmptyInput(t *testing.T) {
	wasmBytes := loadEchoWasm(t)
	rt := NewWazeroRuntime(64)

	out, err := rt.Execute(context.Background(), wasmBytes, []byte{})
	require.NoError(t, err)

	var output TaskOutput
	require.NoError(t, json.Unmarshal(out, &output))
	// Empty input → empty result string
	assert.Equal(t, "", output.Result)
}

func TestWazeroRuntime_Execute_Timeout(t *testing.T) {
	wasmBytes := loadEchoWasm(t)
	rt := NewWazeroRuntime(64)

	// Use an already-cancelled context to guarantee timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := rt.Execute(ctx, wasmBytes, []byte(`{"params":{}}`))
	assert.Error(t, err)
}

func TestWazeroRuntime_Execute_InvalidModule(t *testing.T) {
	rt := NewWazeroRuntime(64)
	_, err := rt.Execute(context.Background(), []byte("not a wasm module"), []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "compile")
}

func TestWazeroRuntime_ExecuteFunc_Integration(t *testing.T) {
	// Test that ExecuteFunc() works with NewExecutor
	wasmBytes := loadEchoWasm(t)
	rt := NewWazeroRuntime(64)

	executor := NewExecutor(DefaultSandboxConfig(), rt.ExecuteFunc())
	out, err := executor.Execute(context.Background(), wasmBytes, TaskInput{
		Params: map[string]string{"key": "value"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out.Result)
	assert.Contains(t, out.Result, "key")
	assert.Contains(t, out.Result, "value")
}

func TestWazeroRuntime_Pipeline_Integration(t *testing.T) {
	// Pipeline with same echo module repeated — output of step N becomes input of step N+1
	wasmBytes := loadEchoWasm(t)
	rt := NewWazeroRuntime(64)

	executor := NewExecutor(DefaultSandboxConfig(), rt.ExecuteFunc())
	out, err := executor.Pipeline(context.Background(), [][]byte{wasmBytes, wasmBytes}, TaskInput{
		Params: map[string]string{"step": "0"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out.Result)
}
