// Package git implements the Git Worker for Forge workflows.
// It performs local git operations via go-git and remote operations via GitLab API.
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Worker is the Git workflow worker.
type Worker struct {
	config *ProjectConfig
}

// NewWorker creates a Git Worker with the given project configuration.
func NewWorker(cfg *ProjectConfig) *Worker {
	return &Worker{config: cfg}
}

// Execute runs a git action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	// Read actions
	case "pull":
		return w.pull(ctx, params)
	case "log":
		return w.log(ctx, params)
	case "diff":
		return w.diff(ctx, params)
	case "blame":
		return w.blame(ctx, params)
	case "search":
		return w.search(ctx, params)
	// Write actions
	case "create_branch":
		return w.createBranch(ctx, params)
	case "commit":
		return w.commit(ctx, params)
	case "push":
		return w.push(ctx, params)
	case "create_mr":
		return w.createMR(ctx, params)
	default:
		return "", fmt.Errorf("git worker: unknown action %q", action)
	}
}

// runGit executes a git command in the project's local path.
func (w *Worker) runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = w.config.LocalPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}
