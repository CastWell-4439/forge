package wasm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fake Wasm magic header for testing.
var fakeWasmModule = append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, []byte("fake-wasm-body")...)

// --- Executor tests ---

func TestExecutorBasic(t *testing.T) {
	expected := TaskOutput{Result: "hello", Artifacts: []string{"out.txt"}}
	expectedBytes, _ := json.Marshal(expected)

	executor := NewExecutor(DefaultSandboxConfig(), func(ctx context.Context, wasmBytes, input []byte) ([]byte, error) {
		// Verify input is valid JSON.
		var ti TaskInput
		require.NoError(t, json.Unmarshal(input, &ti))
		assert.Equal(t, "world", ti.Params["name"])
		return expectedBytes, nil
	})

	output, err := executor.Execute(context.Background(), fakeWasmModule, TaskInput{
		Params: map[string]string{"name": "world"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", output.Result)
	assert.Equal(t, []string{"out.txt"}, output.Artifacts)
}

func TestExecutorTimeout(t *testing.T) {
	cfg := DefaultSandboxConfig()
	cfg.Timeout = 50 * time.Millisecond

	executor := NewExecutor(cfg, func(ctx context.Context, wasmBytes, input []byte) ([]byte, error) {
		// Simulate a slow Wasm module.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, nil
		}
	})

	_, err := executor.Execute(context.Background(), fakeWasmModule, TaskInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestExecutorOutputSizeLimit(t *testing.T) {
	cfg := DefaultSandboxConfig()
	cfg.MaxOutputBytes = 10 // very small limit

	executor := NewExecutor(cfg, func(ctx context.Context, wasmBytes, input []byte) ([]byte, error) {
		return []byte(`{"result":"this is way too long for the limit"}`), nil
	})

	_, err := executor.Execute(context.Background(), fakeWasmModule, TaskInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output size")
}

func TestExecutorEmptyModule(t *testing.T) {
	executor := NewExecutor(DefaultSandboxConfig(), nil)
	_, err := executor.Execute(context.Background(), nil, TaskInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty module")
}

func TestExecutorTaskError(t *testing.T) {
	executor := NewExecutor(DefaultSandboxConfig(), func(ctx context.Context, wasmBytes, input []byte) ([]byte, error) {
		te := TaskError{Transient: true, Message: "temporary failure"}
		b, _ := json.Marshal(te)
		return b, nil
	})

	_, err := executor.Execute(context.Background(), fakeWasmModule, TaskInput{})
	require.Error(t, err)
	var taskErr *TaskError
	assert.ErrorAs(t, err, &taskErr)
	assert.True(t, taskErr.Transient)
}

func TestExecutorStubRuntime(t *testing.T) {
	executor := NewExecutor(DefaultSandboxConfig(), nil)
	out, err := executor.Execute(context.Background(), fakeWasmModule, TaskInput{Params: map[string]string{"key": "val"}})
	require.NoError(t, err)
	// Stub echoes input, so output should contain the serialized input.
	assert.NotEmpty(t, out.Result)
}

// --- Validate module tests ---

func TestValidateModuleValid(t *testing.T) {
	assert.NoError(t, ValidateModule(fakeWasmModule))
}

func TestValidateModuleTooSmall(t *testing.T) {
	assert.Error(t, ValidateModule([]byte{0x00}))
}

func TestValidateModuleBadMagic(t *testing.T) {
	assert.Error(t, ValidateModule([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}))
}

// --- Registry tests ---

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	err := reg.Register("transform", "1.0.0", "data transform plugin", fakeWasmModule)
	require.NoError(t, err)

	wasmBytes, err := reg.Get("transform")
	require.NoError(t, err)
	assert.Equal(t, fakeWasmModule, wasmBytes)
}

func TestRegistryVersioning(t *testing.T) {
	reg := NewRegistry()

	v1Module := append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, []byte("v1")...)
	v2Module := append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, []byte("v2")...)

	require.NoError(t, reg.Register("myplugin", "1.0.0", "test", v1Module))
	time.Sleep(1 * time.Millisecond) // ensure different CreatedAt
	require.NoError(t, reg.Register("myplugin", "2.0.0", "", v2Module))

	// Active should be latest (2.0.0).
	wasmBytes, err := reg.Get("myplugin")
	require.NoError(t, err)
	assert.Equal(t, v2Module, wasmBytes)

	// Can retrieve specific version.
	v1Bytes, err := reg.GetVersion("myplugin", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, v1Module, v1Bytes)

	// Switch active version.
	require.NoError(t, reg.SetActive("myplugin", "1.0.0"))
	wasmBytes, err = reg.Get("myplugin")
	require.NoError(t, err)
	assert.Equal(t, v1Module, wasmBytes)
}

func TestRegistryDuplicateVersion(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register("p", "1.0.0", "", fakeWasmModule))
	err := reg.Register("p", "1.0.0", "", fakeWasmModule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRegistryInvalidModule(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register("bad", "1.0.0", "", []byte{0x01, 0x02})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too small")
}

func TestRegistryNotFound(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("nonexistent")
	assert.Error(t, err)
}

func TestRegistryList(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register("alpha", "1.0.0", "", fakeWasmModule))
	require.NoError(t, reg.Register("beta", "1.0.0", "", fakeWasmModule))

	plugins := reg.List()
	assert.Len(t, plugins, 2)
	assert.Equal(t, "alpha", plugins[0].Name) // sorted
	assert.Equal(t, "beta", plugins[1].Name)
}

func TestRegistryRemove(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register("temp", "1.0.0", "", fakeWasmModule))
	assert.Equal(t, 1, reg.Count())

	require.NoError(t, reg.Remove("temp"))
	assert.Equal(t, 0, reg.Count())

	err := reg.Remove("temp")
	assert.Error(t, err)
}

func TestRegistrySetActiveNotFound(t *testing.T) {
	reg := NewRegistry()
	err := reg.SetActive("nonexistent", "1.0.0")
	assert.Error(t, err)

	require.NoError(t, reg.Register("p", "1.0.0", "", fakeWasmModule))
	err = reg.SetActive("p", "9.9.9")
	assert.Error(t, err)
}
