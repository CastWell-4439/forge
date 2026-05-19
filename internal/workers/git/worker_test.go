package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorker_Execute_UnknownAction(t *testing.T) {
	w := NewWorker(&ProjectConfig{LocalPath: "."})
	_, err := w.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestProjectConfig_Load(t *testing.T) {
	content := `
name: test-project
local_path: /tmp/test
remote_url: git@example.com:test.git
main_branch: main
test_target: dev-offline
gitlab_url: https://gitlab.example.com
gitlab_token: test-token
project_id: "123"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadProjectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "test-project" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.LocalPath != "/tmp/test" {
		t.Errorf("local_path = %q", cfg.LocalPath)
	}
	if cfg.MainBranch != "main" {
		t.Errorf("main_branch = %q", cfg.MainBranch)
	}
	if cfg.TestTarget != "dev-offline" {
		t.Errorf("test_target = %q", cfg.TestTarget)
	}
}

func TestProjectConfig_Defaults(t *testing.T) {
	content := `
name: minimal
local_path: /tmp/min
`
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadProjectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MainBranch != "develop" {
		t.Errorf("default main_branch = %q, want develop", cfg.MainBranch)
	}
	if cfg.TestTarget != "dev-offline" {
		t.Errorf("default test_target = %q, want dev-offline", cfg.TestTarget)
	}
}

func TestProjectConfig_MissingLocalPath(t *testing.T) {
	content := `name: bad`
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	os.WriteFile(path, []byte(content), 0644)

	_, err := LoadProjectConfig(path)
	if err == nil {
		t.Fatal("expected error for missing local_path")
	}
}
