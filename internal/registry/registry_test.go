package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Parser Tests ---

func TestParse_ValidWorkflow(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	wf, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if wf.APIVersion != "forge/v1" {
		t.Errorf("apiVersion = %q, want forge/v1", wf.APIVersion)
	}
	if wf.Kind != "Workflow" {
		t.Errorf("kind = %q, want Workflow", wf.Kind)
	}
	if wf.Metadata.Name != "bug_fix" {
		t.Errorf("name = %q, want bug_fix", wf.Metadata.Name)
	}
	if len(wf.Stages) != 6 {
		t.Errorf("stages count = %d, want 6", len(wf.Stages))
	}
	if len(wf.Triggers) != 1 {
		t.Errorf("triggers count = %d, want 1", len(wf.Triggers))
	}
}

func TestParse_MissingAPIVersion(t *testing.T) {
	data := []byte(`kind: Workflow
metadata:
  name: test
stages:
  - name: s1
    tasks:
      - worker: ai
        action: do`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for missing apiVersion")
	}
}

func TestParse_MissingStages(t *testing.T) {
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: test
stages: []`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for empty stages")
	}
}

func TestParse_DuplicateStageNames(t *testing.T) {
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: test
stages:
  - name: build
    tasks:
      - worker: shell
        action: run
  - name: build
    tasks:
      - worker: shell
        action: run`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for duplicate stage name")
	}
}

func TestParse_MissingWorker(t *testing.T) {
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: test
stages:
  - name: s1
    tasks:
      - action: do`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for missing worker")
	}
}

func TestParse_InvalidKind(t *testing.T) {
	data := []byte(`apiVersion: forge/v1
kind: Job
metadata:
  name: test
stages:
  - name: s1
    tasks:
      - worker: ai
        action: do`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

// --- Compile Tests ---

func TestCompile_ValidWorkflow(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	cw, err := Compile(data)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Name != "bug_fix" {
		t.Errorf("name = %q, want bug_fix", cw.Name)
	}
	if cw.Config.Timeout != 30*time.Minute {
		t.Errorf("timeout = %v, want 30m", cw.Config.Timeout)
	}
	if len(cw.Triggers) != 1 {
		t.Fatalf("triggers count = %d, want 1", len(cw.Triggers))
	}
	if cw.Triggers[0].Interval != 2*time.Minute {
		t.Errorf("trigger interval = %v, want 2m", cw.Triggers[0].Interval)
	}
}

func TestCompile_InvalidDuration(t *testing.T) {
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: test
config:
  timeout: "bad"
stages:
  - name: s1
    tasks:
      - worker: ai
        action: do`)
	_, err := Compile(data)
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

// --- DAG Compiler Tests ---

func TestCompileDAG_TopologicalOrder(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	cw, err := Compile(data)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	g, err := CompileDAG(cw)
	if err != nil {
		t.Fatalf("CompileDAG: %v", err)
	}

	order, err := g.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}

	// Verify order respects stage ordering
	stageOrder := map[string]int{
		"investigate": 0, "analyze": 1, "plan": 2,
		"approve": 3, "execute": 4, "deliver": 5,
	}
	maxSeen := -1
	for _, node := range order {
		idx := stageOrder[node.StageName]
		if idx < maxSeen {
			t.Errorf("node %s (stage %s, order %d) appeared after stage order %d",
				node.ID, node.StageName, idx, maxSeen)
		}
		if idx > maxSeen {
			maxSeen = idx
		}
	}
}

func TestCompileDAG_ParallelStage(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	cw, err := Compile(data)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	g, err := CompileDAG(cw)
	if err != nil {
		t.Fatalf("CompileDAG: %v", err)
	}

	// "investigate" is parallel with 3 tasks — they should all be ready initially
	ready := g.ReadyNodes(map[string]bool{})
	if len(ready) != 3 {
		t.Errorf("initial ready nodes = %d, want 3 (investigate parallel)", len(ready))
	}
	for _, n := range ready {
		if n.StageName != "investigate" {
			t.Errorf("initial ready node %s in stage %s, want investigate", n.ID, n.StageName)
		}
	}
}

func TestCompileDAG_SequentialStage(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	cw, err := Compile(data)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	g, err := CompileDAG(cw)
	if err != nil {
		t.Fatalf("CompileDAG: %v", err)
	}

	// "execute" has 3 sequential tasks: git, claude_code, shell
	// After investigate(3) + analyze(1) + plan(1) + approve(2) all done,
	// only execute.0 should be ready
	completed := map[string]bool{
		"investigate.0": true, "investigate.1": true, "investigate.2": true,
		"analyze.0": true, "plan.0": true,
		"approve.0": true, "approve.1": true,
	}
	ready := g.ReadyNodes(completed)
	if len(ready) != 1 {
		t.Fatalf("ready after approve = %d, want 1", len(ready))
	}
	if ready[0].ID != "execute.0" {
		t.Errorf("expected execute.0, got %s", ready[0].ID)
	}
}

func TestCompileDAG_NodeCount(t *testing.T) {
	data := readTestFile(t, "../../workflows/bug_fix.yaml")
	cw, err := Compile(data)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	g, err := CompileDAG(cw)
	if err != nil {
		t.Fatalf("CompileDAG: %v", err)
	}
	// investigate:3 + analyze:1 + plan:1 + approve:2 + execute:3 + deliver:2 = 12
	if len(g.Nodes) != 12 {
		t.Errorf("node count = %d, want 12", len(g.Nodes))
	}
}

// --- Template Tests ---

func TestRenderString_Simple(t *testing.T) {
	ctx := TemplateContext{
		"inputs": map[string]any{"work_item_id": "BUG-123"},
	}
	result, err := RenderString("fix/{{.inputs.work_item_id}}", ctx)
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if result != "fix/BUG-123" {
		t.Errorf("got %q, want fix/BUG-123", result)
	}
}

func TestRenderString_NoTemplate(t *testing.T) {
	result, err := RenderString("plain text", nil)
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if result != "plain text" {
		t.Errorf("got %q, want 'plain text'", result)
	}
}

func TestRenderParams_Nested(t *testing.T) {
	ctx := TemplateContext{
		"plan": map[string]any{"summary": "fix null pointer"},
	}
	params := map[string]any{
		"message": "方案：{{.plan.summary}}",
		"count":   42,
		"nested":  map[string]any{"key": "{{.plan.summary}}"},
	}
	result, err := RenderParams(params, ctx)
	if err != nil {
		t.Fatalf("RenderParams: %v", err)
	}
	if result["message"] != "方案：fix null pointer" {
		t.Errorf("message = %q", result["message"])
	}
	if result["count"] != 42 {
		t.Errorf("count = %v", result["count"])
	}
	nested := result["nested"].(map[string]any)
	if nested["key"] != "fix null pointer" {
		t.Errorf("nested.key = %q", nested["key"])
	}
}

func TestRenderInputs(t *testing.T) {
	ctx := TemplateContext{
		"event": map[string]any{"work_item_id": "WI-456", "project_key": "avp"},
	}
	inputs := map[string]string{
		"work_item_id": "{{.event.work_item_id}}",
		"project":      "{{.event.project_key}}",
	}
	result, err := RenderInputs(inputs, ctx)
	if err != nil {
		t.Fatalf("RenderInputs: %v", err)
	}
	if result["work_item_id"] != "WI-456" {
		t.Errorf("work_item_id = %v", result["work_item_id"])
	}
	if result["project"] != "avp" {
		t.Errorf("project = %v", result["project"])
	}
}

// --- Registry Tests ---

func TestRegistry_LoadAndGet(t *testing.T) {
	reg := NewRegistry()
	err := reg.Load("../../workflows")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Count() == 0 {
		t.Fatal("registry is empty after Load")
	}
	wf, err := reg.Get("bug_fix")
	if err != nil {
		t.Fatalf("Get(bug_fix): %v", err)
	}
	if wf.Name != "bug_fix" {
		t.Errorf("name = %q", wf.Name)
	}
}

func TestRegistry_GetNotFound(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing workflow")
	}
}

func TestRegistry_Reload(t *testing.T) {
	// Create a temp dir with a valid workflow
	dir := t.TempDir()
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: temp_wf
  version: "1.0"
stages:
  - name: build
    tasks:
      - worker: shell
        action: run
        params:
          command: "make build"`)
	path := filepath.Join(dir, "temp.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	if err := reg.Load(dir); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Count() != 1 {
		t.Fatalf("count = %d, want 1", reg.Count())
	}

	// Modify and reload
	data2 := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: temp_wf
  version: "2.0"
stages:
  - name: test
    tasks:
      - worker: shell
        action: run
        params:
          command: "make test"`)
	if err := os.WriteFile(path, data2, 0644); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(path); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	wf, err := reg.Get("temp_wf")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wf.Version != "2.0" {
		t.Errorf("version = %q, want 2.0", wf.Version)
	}
}

// --- Watcher Test (basic start/stop) ---

func TestWatcher_StartStop(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`apiVersion: forge/v1
kind: Workflow
metadata:
  name: watch_test
stages:
  - name: s1
    tasks:
      - worker: ai
        action: analyze`)
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	if err := reg.Load(dir); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(reg, dir, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Watch should exit when context is cancelled
	err = w.Watch(ctx)
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Watch: %v", err)
	}
}

// --- Helpers ---

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
