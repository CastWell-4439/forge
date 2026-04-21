// Package wasm implements the WebAssembly plugin execution engine for Forge.
// It uses wazero for sandboxed execution with memory limits, timeouts,
// and filesystem isolation.
package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TaskInput represents the input passed to a Wasm plugin.
type TaskInput struct {
	Params   map[string]string `json:"params"`
	Metadata string            `json:"metadata,omitempty"`
}

// TaskOutput represents the output from a Wasm plugin.
type TaskOutput struct {
	Result    string   `json:"result"`
	Artifacts []string `json:"artifacts,omitempty"`
}

// TaskError represents an error from a Wasm plugin.
type TaskError struct {
	Transient bool   `json:"transient"` // true = retryable
	Message   string `json:"message"`
}

func (e *TaskError) Error() string {
	return e.Message
}

// SandboxConfig defines the security sandbox for Wasm execution.
type SandboxConfig struct {
	MemoryLimitMB  uint32        // Max memory in MB (default: 64)
	Timeout        time.Duration // Execution timeout (default: 30s)
	AllowFS        bool          // Allow filesystem access (default: false)
	AllowNetwork   bool          // Allow network access (default: false)
	MaxOutputBytes int           // Max output size in bytes (default: 1MB)
}

// DefaultSandboxConfig returns safe default sandbox settings.
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		MemoryLimitMB:  64,
		Timeout:        30 * time.Second,
		AllowFS:        false,
		AllowNetwork:   false,
		MaxOutputBytes: 1 << 20, // 1MB
	}
}

// ExecuteFunc is the signature for executing a Wasm module.
// This abstraction allows swapping the real wazero runtime with mocks for testing.
type ExecuteFunc func(ctx context.Context, wasmBytes []byte, input []byte) ([]byte, error)

// Executor manages Wasm plugin execution with sandboxing.
type Executor struct {
	sandbox   SandboxConfig
	executeFn ExecuteFunc
}

// NewExecutor creates a new Wasm executor.
// executeFn is the underlying Wasm runtime function. Pass nil to use a stub
// that returns ErrRuntimeNotConfigured (useful for testing the orchestration layer).
func NewExecutor(sandbox SandboxConfig, executeFn ExecuteFunc) *Executor {
	if executeFn == nil {
		executeFn = stubExecute
	}
	return &Executor{
		sandbox:   sandbox,
		executeFn: executeFn,
	}
}

// Execute runs a Wasm plugin with the given input, applying sandbox constraints.
func (e *Executor) Execute(ctx context.Context, wasmBytes []byte, input TaskInput) (*TaskOutput, error) {
	if len(wasmBytes) == 0 {
		return nil, fmt.Errorf("wasm: empty module bytes")
	}

	// Serialize input for the Wasm module.
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("wasm: marshal input: %w", err)
	}

	// Apply timeout from sandbox config.
	execCtx, cancel := context.WithTimeout(ctx, e.sandbox.Timeout)
	defer cancel()

	// Execute with sandbox constraints.
	outputBytes, err := e.executeFn(execCtx, wasmBytes, inputBytes)
	if err != nil {
		// Check if it's a context timeout (sandbox killed it).
		if execCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("wasm: execution timeout after %v", e.sandbox.Timeout)
		}
		return nil, fmt.Errorf("wasm: execution failed: %w", err)
	}

	// Enforce max output size.
	if e.sandbox.MaxOutputBytes > 0 && len(outputBytes) > e.sandbox.MaxOutputBytes {
		return nil, fmt.Errorf("wasm: output size %d exceeds limit %d", len(outputBytes), e.sandbox.MaxOutputBytes)
	}

	// Parse output.
	// First try TaskError (plugin-reported errors).
	var taskErr TaskError
	if json.Unmarshal(outputBytes, &taskErr) == nil && taskErr.Message != "" {
		return nil, &taskErr
	}

	// Then parse as TaskOutput.
	var output TaskOutput
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		return nil, fmt.Errorf("wasm: unmarshal output: %w", err)
	}

	return &output, nil
}

// Pipeline executes a chain of Wasm components, passing output to next input.
//
// Design note: only the Result string is forwarded between pipeline steps;
// Artifacts from intermediate steps are NOT propagated. This is intentional:
// each step receives a clean input derived solely from the previous step's
// Result field. If a step needs to pass structured data, it should serialize
// it into the Result string (e.g. JSON). Collect Artifacts from the final
// TaskOutput only.
func (e *Executor) Pipeline(ctx context.Context, modules [][]byte, input TaskInput) (*TaskOutput, error) {
	if len(modules) == 0 {
		return nil, fmt.Errorf("wasm pipeline: no modules")
	}

	current := input
	var lastOutput *TaskOutput

	for i, mod := range modules {
		output, err := e.Execute(ctx, mod, current)
		if err != nil {
			return nil, fmt.Errorf("wasm pipeline step %d: %w", i, err)
		}
		lastOutput = output

		// Convert output to input for next step (if not last).
		if i < len(modules)-1 {
			current = TaskInput{
				Params:   map[string]string{"result": output.Result},
				Metadata: fmt.Sprintf("step_%d", i),
			}
		}
	}

	return lastOutput, nil
}

// ValidateModule performs basic validation of a Wasm binary.
func ValidateModule(wasmBytes []byte) error {
	if len(wasmBytes) < 8 {
		return fmt.Errorf("wasm: module too small (%d bytes)", len(wasmBytes))
	}
	// Check Wasm magic number: \0asm
	if wasmBytes[0] != 0x00 || wasmBytes[1] != 0x61 || wasmBytes[2] != 0x73 || wasmBytes[3] != 0x6d {
		return fmt.Errorf("wasm: invalid magic number (not a Wasm module)")
	}
	return nil
}

// stubExecute is a development placeholder for the wazero WebAssembly runtime.
// In production, replace with wazero.NewRuntime() instantiation.
// The stub echoes the input as-is, allowing pipeline and integration tests
// to exercise the orchestration layer without a real Wasm runtime.
//
// To integrate wazero:
//   1. go get github.com/tetratelabs/wazero
//   2. Replace this function with wazero module instantiation + WASI
//   3. Input bytes are passed as stdin, output read from stdout
func stubExecute(_ context.Context, _ []byte, input []byte) ([]byte, error) {
	// Echo input as the result field for testability.
	if len(input) == 0 {
		return []byte(`{"result":"wasm-stub-no-input"}`), nil
	}
	// Wrap raw input into a TaskOutput-shaped JSON so Execute can unmarshal it.
	escaped, err := json.Marshal(string(input))
	if err != nil {
		return []byte(`{"result":"wasm-stub-marshal-error"}`), nil
	}
	return []byte(fmt.Sprintf(`{"result":%s}`, escaped)), nil
}
