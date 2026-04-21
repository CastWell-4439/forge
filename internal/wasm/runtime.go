// Package wasm — WazeroRuntime provides real WebAssembly execution via wazero.
package wasm

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// WazeroRuntime wraps a wazero.Runtime for executing WASI command modules.
// Plugin protocol:
//   - Entry: _start (WASI command module)
//   - Input:  stdin  → JSON (TaskInput)
//   - Output: stdout → JSON (TaskOutput or TaskError)
//   - Exit code 0 = success, non-zero = transient error
type WazeroRuntime struct {
	memoryLimitPages uint32 // 1 page = 64 KB
}

// NewWazeroRuntime creates a runtime with the given memory limit in MB.
// Pass 0 for the default (64 MB).
func NewWazeroRuntime(memoryLimitMB uint32) *WazeroRuntime {
	if memoryLimitMB == 0 {
		memoryLimitMB = 64
	}
	return &WazeroRuntime{
		memoryLimitPages: memoryLimitMB * 16, // 1 MB = 16 pages (16 * 64KB)
	}
}

// Execute satisfies the ExecuteFunc signature.
// It compiles the Wasm module, feeds input via stdin, and captures stdout.
func (r *WazeroRuntime) Execute(ctx context.Context, wasmBytes []byte, input []byte) ([]byte, error) {
	// Fail fast if context is already done.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("wazero: context already done: %w", err)
	}

	// Create a fresh runtime per invocation for isolation.
	// wazero.NewRuntime compiles in-memory; for repeated modules consider caching.
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(r.memoryLimitPages)

	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer rt.Close(ctx)

	// Instantiate WASI to provide stdin/stdout/stderr and proc_exit.
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	// Prepare stdin/stdout buffers.
	stdin := bytes.NewReader(input)
	var stdout, stderr bytes.Buffer

	modCfg := wazero.NewModuleConfig().
		WithStdin(stdin).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithName("plugin")

	// Compile + instantiate. Instantiation calls _start automatically for
	// WASI command modules, blocking until the guest returns.
	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("wazero: compile: %w", err)
	}

	_, err = rt.InstantiateModule(ctx, compiled, modCfg)
	if err != nil {
		// If the module wrote something to stdout before failing, return it
		// so the caller can inspect partial output.
		if stdout.Len() > 0 {
			return stdout.Bytes(), fmt.Errorf("wazero: instantiate (partial output): %w", err)
		}
		return nil, fmt.Errorf("wazero: instantiate: %w", err)
	}

	if stdout.Len() == 0 {
		return nil, fmt.Errorf("wazero: module produced no output on stdout")
	}

	return stdout.Bytes(), nil
}

// ExecuteFunc returns an ExecuteFunc-compatible closure for use with NewExecutor.
func (r *WazeroRuntime) ExecuteFunc() ExecuteFunc {
	return r.Execute
}
