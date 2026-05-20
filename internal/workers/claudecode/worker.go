// Package claudecode implements the Claude Code Worker for Forge workflows.
// It invokes the Claude Code CLI in --print mode to execute code modifications
// within a controlled environment (feature/fix branches only).
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Config holds Claude Code Worker configuration.
type Config struct {
	// ClaudePath is the path to the claude CLI binary.
	// Defaults to "claude" (assumes it's in PATH).
	ClaudePath string

	// DefaultTimeout per execution.
	DefaultTimeout time.Duration

	// AllowedBranchPrefixes restricts which branches can be modified.
	AllowedBranchPrefixes []string

	// ForbiddenPaths are file patterns that must never be touched.
	ForbiddenPaths []string

	// MaxOutputBytes limits stdout capture size.
	MaxOutputBytes int
}

// DefaultConfig returns a safe default configuration.
func DefaultConfig() Config {
	return Config{
		ClaudePath:     "claude",
		DefaultTimeout: 5 * time.Minute,
		AllowedBranchPrefixes: []string{
			"feature/",
			"fix/",
			"refactor/",
		},
		ForbiddenPaths: []string{
			"*.env",
			"*secret*",
			"*config/prod*",
			"*credentials*",
		},
		MaxOutputBytes: 512 * 1024, // 512KB
	}
}

// Worker is the Claude Code workflow worker.
type Worker struct {
	config Config
	// cmdFactory allows test injection. If nil, uses exec.CommandContext.
	cmdFactory CmdFactory
}

// CmdFactory creates exec.Cmd instances (for testing).
type CmdFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// NewWorker creates a Claude Code Worker.
func NewWorker(cfg Config) *Worker {
	return &Worker{config: cfg}
}

// NewWorkerWithFactory creates a Claude Code Worker with custom command factory (for testing).
func NewWorkerWithFactory(cfg Config, factory CmdFactory) *Worker {
	return &Worker{config: cfg, cmdFactory: factory}
}

// Execute runs a Claude Code action.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "implement":
		return w.implement(ctx, params)
	case "fix":
		return w.fix(ctx, params)
	case "refactor":
		return w.refactor(ctx, params)
	case "add_test":
		return w.addTest(ctx, params)
	default:
		return "", fmt.Errorf("claudecode worker: unknown action %q", action)
	}
}

// execClaude invokes the claude CLI with --print mode.
func (w *Worker) execClaude(ctx context.Context, workdir string, prompt string) (string, error) {
	timeout := w.config.DefaultTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"--print",
		"--permission-mode", "bypassPermissions",
		prompt,
	}

	var cmd *exec.Cmd
	if w.cmdFactory != nil {
		cmd = w.cmdFactory(ctx, w.config.ClaudePath, args...)
	} else {
		cmd = exec.CommandContext(ctx, w.config.ClaudePath, args...)
	}
	cmd.Dir = workdir

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Truncate output for error message.
		outStr := string(out)
		if len(outStr) > 2000 {
			outStr = outStr[:2000] + "\n...(truncated)"
		}
		return "", fmt.Errorf("claude exec failed: %w\noutput: %s", err, outStr)
	}

	result := string(out)
	if w.config.MaxOutputBytes > 0 && len(result) > w.config.MaxOutputBytes {
		result = result[:w.config.MaxOutputBytes] + "\n...(truncated)"
	}
	return result, nil
}

// validateBranch checks that the current branch is allowed.
func (w *Worker) validateBranch(ctx context.Context, workdir string) error {
	var cmd *exec.Cmd
	if w.cmdFactory != nil {
		cmd = w.cmdFactory(ctx, "git", "branch", "--show-current")
	} else {
		cmd = exec.CommandContext(ctx, "git", "branch", "--show-current")
	}
	cmd.Dir = workdir

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	branch := strings.TrimSpace(string(out))
	for _, prefix := range w.config.AllowedBranchPrefixes {
		if strings.HasPrefix(branch, prefix) {
			return nil
		}
	}
	return fmt.Errorf("branch %q not allowed (must start with %v)", branch, w.config.AllowedBranchPrefixes)
}

// getParam extracts a string parameter, returning error if missing.
func getParam(params map[string]any, key string) (string, error) {
	v, ok := params[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("missing required param %q", key)
	}
	return v, nil
}

// getOptionalParam extracts a string parameter, returning default if missing.
func getOptionalParam(params map[string]any, key, defaultVal string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

// resultJSON produces a structured JSON result.
func resultJSON(action, status, output string) string {
	r := map[string]string{
		"action": action,
		"status": status,
		"output": output,
	}
	data, _ := json.Marshal(r)
	return string(data)
}
