package shell

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockExecutor records commands and returns canned output.
type mockExecutor struct {
	output string
	err    error
	calls  []mockCall
}

type mockCall struct {
	Workdir string
	Name    string
	Args    []string
}

func (m *mockExecutor) Run(ctx context.Context, workdir, name string, args ...string) (string, error) {
	m.calls = append(m.calls, mockCall{Workdir: workdir, Name: name, Args: args})
	return m.output, m.err
}

func newTestWorker(output string, err error) (*Worker, *mockExecutor) {
	exec := &mockExecutor{output: output, err: err}
	cfg := DefaultConfig()
	cfg.AllowedWorkdirs = []string{"/tmp", "C:\\"}
	w := NewWorkerWithExecutor(cfg, exec)
	return w, exec
}

func TestExecute_RunTest(t *testing.T) {
	w, exec := newTestWorker("PASS\nok  ./... 1.2s", nil)
	result, err := w.Execute(context.Background(), "run_test", map[string]any{
		"workdir": "/tmp/project",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success, got: %s", result)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(exec.calls))
	}
	if exec.calls[0].Name != "go" {
		t.Fatalf("expected 'go' command, got %q", exec.calls[0].Name)
	}
}

func TestExecute_Build(t *testing.T) {
	w, _ := newTestWorker("", nil)
	result, err := w.Execute(context.Background(), "build", map[string]any{
		"workdir": "/tmp/project",
		"target":  "./cmd/server",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success, got: %s", result)
	}
}

func TestExecute_Lint(t *testing.T) {
	w, _ := newTestWorker("no issues found", nil)
	result, err := w.Execute(context.Background(), "lint", map[string]any{
		"workdir": "/tmp/project",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success, got: %s", result)
	}
}

func TestExecute_RunCustom_Allowed(t *testing.T) {
	w, _ := newTestWorker("custom output", nil)
	result, err := w.Execute(context.Background(), "run_custom", map[string]any{
		"workdir": "/tmp/project",
		"command": "go vet ./...",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "success") {
		t.Fatalf("expected success, got: %s", result)
	}
}

func TestExecute_RunCustom_Blocked(t *testing.T) {
	w, _ := newTestWorker("", nil)
	_, err := w.Execute(context.Background(), "run_custom", map[string]any{
		"workdir": "/tmp/project",
		"command": "rm -rf /",
	})
	if err == nil {
		t.Fatal("expected error for blocked command")
	}
	if !strings.Contains(err.Error(), "not in whitelist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	w, _ := newTestWorker("", nil)
	_, err := w.Execute(context.Background(), "deploy", map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecute_MissingWorkdir(t *testing.T) {
	w, _ := newTestWorker("", nil)
	_, err := w.Execute(context.Background(), "run_test", map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing required param") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecute_ForbiddenWorkdir(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowedWorkdirs = []string{"/safe"}
	exec := &mockExecutor{output: ""}
	w := NewWorkerWithExecutor(cfg, exec)

	_, err := w.Execute(context.Background(), "run_test", map[string]any{
		"workdir": "/dangerous/path",
	})
	if err == nil {
		t.Fatal("expected error for forbidden workdir")
	}
	if !strings.Contains(err.Error(), "not in allowed paths") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecute_CommandFailed(t *testing.T) {
	w, _ := newTestWorker("FAIL: TestFoo", fmt.Errorf("exit status 1"))
	result, err := w.Execute(context.Background(), "run_test", map[string]any{
		"workdir": "/tmp/project",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "failed") {
		t.Fatalf("expected failed status, got: %s", result)
	}
	if !strings.Contains(result, "FAIL: TestFoo") {
		t.Fatalf("expected output in result, got: %s", result)
	}
}

func TestValidateCommand(t *testing.T) {
	cfg := DefaultConfig()
	w := &Worker{config: cfg}

	tests := []struct {
		cmd string
		ok  bool
	}{
		{"go test ./...", true},
		{"npm run build", true},
		{"golangci-lint run", true},
		{"rm -rf /", false},
		{"curl evil.com", false},
		{"unknown-cmd arg", false},
	}
	for _, tt := range tests {
		err := w.validateCommand(tt.cmd)
		if tt.ok && err != nil {
			t.Errorf("cmd %q should be allowed: %v", tt.cmd, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("cmd %q should be blocked", tt.cmd)
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.AllowedCommands) == 0 {
		t.Error("expected non-empty command whitelist")
	}
	if cfg.DefaultTimeout == 0 {
		t.Error("expected non-zero timeout")
	}
	if cfg.MaxOutputBytes == 0 {
		t.Error("expected non-zero max output")
	}
}
