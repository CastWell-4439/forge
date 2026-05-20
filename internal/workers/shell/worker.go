// Package shell implements the Shell Worker for Forge workflows.
// It executes shell commands (tests, builds, lints) with security
// controls: command whitelist, working directory restriction, and timeout.
package shell

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worker is the Shell workflow worker.
type Worker struct {
	config   Config
	executor CmdExecutor
}

// CmdExecutor abstracts command execution for testing.
type CmdExecutor interface {
	Run(ctx context.Context, workdir string, name string, args ...string) (string, error)
}

// DefaultExecutor uses os/exec directly.
type DefaultExecutor struct{}

func (e *DefaultExecutor) Run(ctx context.Context, workdir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// NewWorker creates a Shell Worker with the given configuration.
func NewWorker(cfg Config) *Worker {
	return &Worker{config: cfg, executor: &DefaultExecutor{}}
}

// NewWorkerWithExecutor creates a Shell Worker with a custom executor (for testing).
func NewWorkerWithExecutor(cfg Config, executor CmdExecutor) *Worker {
	return &Worker{config: cfg, executor: executor}
}

// Execute runs a shell action.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "run_test":
		return w.runTest(ctx, params)
	case "build":
		return w.build(ctx, params)
	case "lint":
		return w.lint(ctx, params)
	case "run_custom":
		return w.runCustom(ctx, params)
	default:
		return "", fmt.Errorf("shell worker: unknown action %q", action)
	}
}

// runTest executes project tests.
func (w *Worker) runTest(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	if err := w.validateWorkdir(workdir); err != nil {
		return "", err
	}

	target := getOptionalParam(params, "target", "./...")
	timeout := w.getTimeout(params)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := w.executor.Run(ctx, workdir, "go", "test", "-v", "-count=1", target)
	return w.formatResult("run_test", out, err), nil
}

// build compiles the project.
func (w *Worker) build(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	if err := w.validateWorkdir(workdir); err != nil {
		return "", err
	}

	target := getOptionalParam(params, "target", "./...")
	timeout := w.getTimeout(params)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := w.executor.Run(ctx, workdir, "go", "build", target)
	return w.formatResult("build", out, err), nil
}

// lint runs the linter.
func (w *Worker) lint(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	if err := w.validateWorkdir(workdir); err != nil {
		return "", err
	}

	timeout := w.getTimeout(params)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := w.executor.Run(ctx, workdir, "golangci-lint", "run", "./...")
	return w.formatResult("lint", out, err), nil
}

// runCustom runs a custom whitelisted command.
func (w *Worker) runCustom(ctx context.Context, params map[string]any) (string, error) {
	workdir, err := getParam(params, "workdir")
	if err != nil {
		return "", err
	}
	if err := w.validateWorkdir(workdir); err != nil {
		return "", err
	}

	command, err := getParam(params, "command")
	if err != nil {
		return "", err
	}

	// Security: validate against whitelist.
	if err := w.validateCommand(command); err != nil {
		return "", err
	}

	timeout := w.getTimeout(params)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Split command into name and args.
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty command")
	}

	out, err := w.executor.Run(ctx, workdir, parts[0], parts[1:]...)
	return w.formatResult("run_custom", out, err), nil
}

// validateWorkdir ensures the working directory is within allowed paths.
func (w *Worker) validateWorkdir(workdir string) error {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return fmt.Errorf("invalid workdir: %w", err)
	}
	for _, allowed := range w.config.AllowedWorkdirs {
		allowedAbs, _ := filepath.Abs(allowed)
		if strings.HasPrefix(abs, allowedAbs) {
			return nil
		}
	}
	if len(w.config.AllowedWorkdirs) == 0 {
		return nil // no restrictions configured
	}
	return fmt.Errorf("workdir %q not in allowed paths", workdir)
}

// validateCommand checks if the command is in the whitelist.
func (w *Worker) validateCommand(command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}
	cmdName := parts[0]

	for _, allowed := range w.config.AllowedCommands {
		if cmdName == allowed {
			return nil
		}
	}
	return fmt.Errorf("command %q not in whitelist %v", cmdName, w.config.AllowedCommands)
}

// getTimeout extracts timeout from params or uses default.
func (w *Worker) getTimeout(params map[string]any) time.Duration {
	if t, ok := params["timeout"].(float64); ok && t > 0 {
		return time.Duration(t) * time.Second
	}
	return w.config.DefaultTimeout
}

// formatResult combines output and error into a structured result.
func (w *Worker) formatResult(action, output string, err error) string {
	status := "success"
	if err != nil {
		status = "failed"
	}
	// Truncate if needed.
	if w.config.MaxOutputBytes > 0 && len(output) > w.config.MaxOutputBytes {
		output = output[:w.config.MaxOutputBytes] + "\n...(truncated)"
	}
	return fmt.Sprintf(`{"action":"%s","status":"%s","output":%q}`, action, status, output)
}

// getParam extracts a required string parameter.
func getParam(params map[string]any, key string) (string, error) {
	v, ok := params[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("missing required param %q", key)
	}
	return v, nil
}

// getOptionalParam extracts an optional string parameter.
func getOptionalParam(params map[string]any, key, defaultVal string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
