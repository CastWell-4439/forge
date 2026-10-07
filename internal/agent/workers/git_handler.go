package workers

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Git handler tool definitions: git.status, git.log, git.diff.
//
// Read-only by construction: each tool runs one fixed git subcommand, so a
// parameter can choose a repository or a path but never a different operation.
// Writes belong to the workflow-plane git worker, not to the agent.

// --- Definitions ---

func GitStatusDef() *ToolDef {
	return &ToolDef{
		Name:        "git.status",
		DisplayName: "Git Status",
		Category:    "git",
		Description: "Show the working tree status of a repository: current branch and changed files.",
		InputSchema: map[string]ParamDef{
			"repo": {Type: "string", Description: "Repository directory (default: workspace)"},
		},
		OutputSchema: map[string]ParamDef{
			"output": {Type: "string", Description: "Short-format git status output"},
		},
		Effect:        EffectRead,
		EstimatedTime: 1 * time.Second,
	}
}

func GitLogDef() *ToolDef {
	return &ToolDef{
		Name:        "git.log",
		DisplayName: "Git Log",
		Category:    "git",
		Description: "Show recent commits of a repository.",
		InputSchema: map[string]ParamDef{
			"repo":  {Type: "string", Description: "Repository directory (default: workspace)"},
			"count": {Type: "integer", Description: "Number of commits to show (default 10, max 100)"},
		},
		OutputSchema: map[string]ParamDef{
			"output": {Type: "string", Description: "Oneline git log output"},
		},
		Effect:        EffectRead,
		EstimatedTime: 1 * time.Second,
	}
}

func GitDiffDef() *ToolDef {
	return &ToolDef{
		Name:        "git.diff",
		DisplayName: "Git Diff",
		Category:    "git",
		Description: "Show the diff of a repository against HEAD.",
		InputSchema: map[string]ParamDef{
			"repo": {Type: "string", Description: "Repository directory (default: workspace)"},
			"path": {Type: "string", Description: "Limit the diff to one path (optional)"},
		},
		OutputSchema: map[string]ParamDef{
			"output": {Type: "string", Description: "Unified diff output"},
		},
		Effect:        EffectRead,
		EstimatedTime: 1 * time.Second,
	}
}

// --- Handler constructors ---

func NewGitStatusHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockGitStatus()
	}
	return realGitOps(cfg, "git.status", []string{"status", "--short", "--branch"})
}

func NewGitLogHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockGitLog()
	}
	return HandlerFunc(func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		count, ok := toInt(params["count"], 10)
		if !ok {
			count = 10
		}
		if count < 1 {
			count = 1
		}
		if count > 100 {
			count = 100
		}
		args := []string{"log", fmt.Sprintf("-%d", count), "--oneline"}
		out, err := runGit(ctx, cfg.Workspace, params, args)
		if err != nil {
			return nil, fmt.Errorf("git.log: %w", err)
		}
		return map[string]interface{}{"output": out}, nil
	})
}

func NewGitDiffHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockGitDiff()
	}
	return HandlerFunc(func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		args := []string{"diff", "HEAD"}
		// A user path is appended after "--", where git reads it as a pathspec
		// and never as an option.
		if path, _ := params["path"].(string); path != "" {
			args = append(args, "--", path)
		}
		out, err := runGit(ctx, cfg.Workspace, params, args)
		if err != nil {
			return nil, fmt.Errorf("git.diff: %w", err)
		}
		return map[string]interface{}{"output": out}, nil
	})
}

// realGitOps builds a handler for a fixed git subcommand.
func realGitOps(cfg HandlerConfig, tool string, args []string) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		out, err := runGit(ctx, cfg.Workspace, params, args)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tool, err)
		}
		return map[string]interface{}{"output": out}, nil
	}
}

// runGit executes git with a fixed argument list inside the requested repo,
// bounded to the workspace.
func runGit(ctx context.Context, workspace string, params map[string]interface{}, args []string) (string, error) {
	repo, _ := params["repo"].(string)
	if repo == "" {
		repo = workspace
	}
	target, err := resolveInWorkspace(workspace, repo)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", target}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(out) > 0 {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), ee, strings.TrimSpace(string(out)))
		}
		return "", err
	}
	return string(out), nil
}

// --- Mocks ---

func mockGitStatus() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{
			"output": "## feature/mock\n M internal/worker/gate.go\n",
		}, nil
	}
}

func mockGitLog() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{
			"output": "a1b2c3d feat: sample commit\ne4f5a6b fix: another sample\n",
		}, nil
	}
}

func mockGitDiff() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{
			"output": "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n",
		}, nil
	}
}
