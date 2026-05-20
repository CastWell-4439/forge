package claudecode

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// mockCmdFactory returns a command factory that simulates git/claude commands.
func mockCmdFactory(output string) CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) > 0 && args[0] == "branch" {
			return exec.CommandContext(ctx, "cmd", "/c", "echo feature/test-branch")
		}
		return exec.CommandContext(ctx, "cmd", "/c", "echo "+output)
	}
}

// mockCmdFactoryWithBranch creates a factory that returns a specific branch.
func mockCmdFactoryWithBranch(branch, claudeOutput string) CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) > 0 && args[0] == "branch" {
			return exec.CommandContext(ctx, "cmd", "/c", "echo "+branch)
		}
		return exec.CommandContext(ctx, "cmd", "/c", "echo "+claudeOutput)
	}
}

func TestWorkerExecute_Implement(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory("code implemented"))
	result, err := w.Execute(context.Background(), "implement", map[string]any{
		"workdir":          t.TempDir(),
		"task_description": "Add login endpoint",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success result, got: %s", result)
	}
}

func TestWorkerExecute_Fix(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory("bug fixed"))
	result, err := w.Execute(context.Background(), "fix", map[string]any{
		"workdir":         t.TempDir(),
		"bug_description": "NullPointerException in UserService",
		"error_log":       "panic at line 42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success result, got: %s", result)
	}
}

func TestWorkerExecute_Refactor(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory("refactored"))
	result, err := w.Execute(context.Background(), "refactor", map[string]any{
		"workdir":       t.TempDir(),
		"refactor_goal": "Extract auth middleware",
		"constraints":   "Must remain backward compatible",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success result, got: %s", result)
	}
}

func TestWorkerExecute_AddTest(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory("tests added"))
	result, err := w.Execute(context.Background(), "add_test", map[string]any{
		"workdir":        t.TempDir(),
		"target_file":    "internal/auth/handler.go",
		"test_framework": "testing",
		"coverage_goal":  "90%",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success result, got: %s", result)
	}
}

func TestWorkerExecute_UnknownAction(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory(""))
	_, err := w.Execute(context.Background(), "deploy", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerExecute_ForbiddenBranch(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactoryWithBranch("main", "should not run"))
	_, err := w.Execute(context.Background(), "implement", map[string]any{
		"workdir":          t.TempDir(),
		"task_description": "something",
	})
	if err == nil {
		t.Fatal("expected error for forbidden branch")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerExecute_MissingWorkdir(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory(""))
	_, err := w.Execute(context.Background(), "implement", map[string]any{
		"task_description": "something",
	})
	if err == nil {
		t.Fatal("expected error for missing workdir")
	}
	if !strings.Contains(err.Error(), "missing required param") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerExecute_MissingTaskDescription(t *testing.T) {
	w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactory(""))
	_, err := w.Execute(context.Background(), "implement", map[string]any{
		"workdir": t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing task_description")
	}
}

func TestValidateBranch_AllowedPrefixes(t *testing.T) {
	tests := []struct {
		branch string
		ok     bool
	}{
		{"feature/new-login", true},
		{"fix/bug-123", true},
		{"refactor/cleanup", true},
		{"main", false},
		{"develop", false},
		{"release/1.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			w := NewWorkerWithFactory(DefaultConfig(), mockCmdFactoryWithBranch(tt.branch, ""))
			err := w.validateBranch(context.Background(), t.TempDir())
			if tt.ok && err != nil {
				t.Fatalf("expected branch %q to be allowed, got: %v", tt.branch, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("expected branch %q to be rejected", tt.branch)
			}
		})
	}
}

func TestBuildPrompts(t *testing.T) {
	p := buildImplementPrompt("add auth", "internal/auth/")
	if !strings.Contains(p, "add auth") || !strings.Contains(p, "internal/auth/") {
		t.Errorf("implement prompt missing content: %s", p)
	}

	p = buildFixPrompt("nil pointer", "panic at line 5", "")
	if !strings.Contains(p, "nil pointer") || !strings.Contains(p, "panic at line 5") {
		t.Errorf("fix prompt missing content: %s", p)
	}

	p = buildRefactorPrompt("extract method", "", "no breaking changes")
	if !strings.Contains(p, "extract method") || !strings.Contains(p, "no breaking changes") {
		t.Errorf("refactor prompt missing content: %s", p)
	}

	p = buildTestPrompt("handler.go", "testify", "95%")
	if !strings.Contains(p, "handler.go") || !strings.Contains(p, "95%") {
		t.Errorf("test prompt missing content: %s", p)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ClaudePath != "claude" {
		t.Errorf("expected claude path 'claude', got %q", cfg.ClaudePath)
	}
	if len(cfg.AllowedBranchPrefixes) != 3 {
		t.Errorf("expected 3 allowed prefixes, got %d", len(cfg.AllowedBranchPrefixes))
	}
	if cfg.MaxOutputBytes != 512*1024 {
		t.Errorf("expected 512KB max output, got %d", cfg.MaxOutputBytes)
	}
}

func TestResultJSON(t *testing.T) {
	r := resultJSON("implement", "success", "done")
	if !strings.Contains(r, `"action":"implement"`) {
		t.Errorf("expected action in JSON: %s", r)
	}
	if !strings.Contains(r, `"status":"success"`) {
		t.Errorf("expected status in JSON: %s", r)
	}
}
