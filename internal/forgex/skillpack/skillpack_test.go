package skillpack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/forgex/cases"
)

// writeRegistryFile writes a one-case registry and loads it through the public
// API, so the tests exercise exactly what the CLI will do.
func writeRegistryFile(t *testing.T, id string) *cases.Registry {
	t.Helper()
	content := `version: 1
cases:
  - id: ` + id + `
    description: replay a verified run
    task_packet: examples/forgex/packet.yaml
    suite: suite_v1
    expected:
      status: stopped
`
	path := filepath.Join(t.TempDir(), "cases.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	reg, err := cases.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// makeRunDir writes the artifacts distillation reads.
func makeRunDir(t *testing.T, caseID string, opts ...func(map[string]string)) string {
	t.Helper()
	dir := t.TempDir()

	extras := map[string]string{}
	for _, opt := range opts {
		opt(extras)
	}

	verdict := map[string]interface{}{"case_id": caseID, "passed": true}
	if v, ok := extras["verdict"]; ok {
		var parsed map[string]interface{}
		_ = json.Unmarshal([]byte(v), &parsed)
		verdict = parsed
	}
	writeJSON(t, filepath.Join(dir, "replay_result.json"), verdict)

	eval := map[string]interface{}{"run_id": "run_x", "suite_id": "suite_v1", "status": "passed"}
	if v, ok := extras["eval"]; ok {
		var parsed map[string]interface{}
		_ = json.Unmarshal([]byte(v), &parsed)
		eval = parsed
	}
	writeJSON(t, filepath.Join(dir, "eval_result.json"), eval)

	toolCalls := []map[string]interface{}{
		{"id": "t1", "run_id": "run_x", "tool_name": "mcp.get_workitem"},
		{"id": "t2", "run_id": "run_x", "tool_name": "git.search"},
		{"id": "t3", "run_id": "run_x", "tool_name": "mcp.get_workitem"}, // duplicate
	}
	writeJSONL(t, filepath.Join(dir, "tool_calls.jsonl"), toolCalls)

	lessons := []map[string]interface{}{
		{"id": "lesson-1", "title": "do not replay blindly", "content": "check the idempotency key first", "source_run_id": "run_x"},
	}
	if noLessons, ok := extras["no_lessons"]; ok && noLessons == "true" {
		// simply do not write the file
	} else {
		writeJSONL(t, filepath.Join(dir, "lessons.jsonl"), lessons)
	}
	return dir
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeJSONL(t *testing.T, path string, items []map[string]interface{}) {
	t.Helper()
	var out string
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		out += string(data) + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadSourceRefusesUnverifiedRuns(t *testing.T) {
	reg := writeRegistryFile(t, "case-a")

	t.Run("no replay verdict", func(t *testing.T) {
		dir := t.TempDir() // no replay_result.json at all
		if _, err := LoadSource(dir, reg); err == nil {
			t.Fatal("expected an error for a run without replay_result.json")
		}
	})

	t.Run("verdict failed", func(t *testing.T) {
		dir := makeRunDir(t, "case-a", func(m map[string]string) {
			m["verdict"] = `{"case_id":"case-a","passed":false}`
		})
		_, err := LoadSource(dir, reg)
		if err == nil {
			t.Fatal("expected an error for a failed verdict")
		}
		if want := "did not match"; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should say the outcome did not match", err)
		}
	})

	t.Run("suite failed", func(t *testing.T) {
		dir := makeRunDir(t, "case-a", func(m map[string]string) {
			m["eval"] = `{"run_id":"run_x","suite_id":"suite_v1","status":"failed"}`
		})
		if _, err := LoadSource(dir, reg); err == nil {
			t.Fatal("expected an error for a failed suite")
		}
	})

	t.Run("case not registered", func(t *testing.T) {
		dir := makeRunDir(t, "case-unknown")
		if _, err := LoadSource(dir, reg); err == nil {
			t.Fatal("expected an error for an unregistered case")
		}
	})
}

func TestDistillIsDeterministic(t *testing.T) {
	reg := writeRegistryFile(t, "case-a")
	src, err := LoadSource(makeRunDir(t, "case-a"), reg)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}

	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first, err := yaml.Marshal(Distill(src, now))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := yaml.Marshal(Distill(src, now))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(first) != string(second) {
		t.Error("distilling the same source twice must produce identical bytes")
	}
}

func TestDistillShapes(t *testing.T) {
	reg := writeRegistryFile(t, "case-a")
	src, err := LoadSource(makeRunDir(t, "case-a"), reg)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	p := Distill(src, time.Now())

	if p.Kind != Kind || p.APIVersion != APIVersion {
		t.Errorf("kind/apiVersion = %s/%s", p.Kind, p.APIVersion)
	}
	if p.Metadata.Status != StatusDraft || p.Metadata.ReviewStatus != ReviewPending {
		t.Errorf("distilled pack must start as a pending draft, got %s/%s", p.Metadata.Status, p.Metadata.ReviewStatus)
	}
	if len(p.Spec.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (duplicated tool collapsed)", len(p.Spec.Steps))
	}
	if p.Spec.Steps[0].Name != "mcp.get_workitem" || p.Spec.Steps[1].Name != "git.search" {
		t.Errorf("step order = %s, %s; first-occurrence order expected", p.Spec.Steps[0].Name, p.Spec.Steps[1].Name)
	}
	if len(p.Spec.Constraints) != 1 || p.Spec.Constraints[0].Evidence != "lesson-1" {
		t.Errorf("constraints = %+v", p.Spec.Constraints)
	}
	if !p.IsPlaceholder() {
		t.Error("distilled readme must be the template the publish gate refuses")
	}
	if p.Spec.Eval.Suite != "suite_v1" || len(p.Spec.Eval.Cases) != 1 {
		t.Errorf("eval binding = %+v", p.Spec.Eval)
	}
}

func TestCheckGates(t *testing.T) {
	existsTrue := func(string) bool { return true }
	good := Pack{
		Readme: "A real description of when to use this.",
		Spec: Spec{
			Trigger:         Trigger{Description: "d"},
			ToolPermissions: []string{"git.status"},
			Steps:           []Step{{Name: "status", Tools: []string{"git.status"}}},
			Eval:            EvalBinding{Suite: "suite_v1", Cases: []string{"case-a"}},
		},
	}
	if issues := good.Check(existsTrue); len(issues) != 0 {
		t.Errorf("a complete pack should pass, got %v", issues)
	}

	t.Run("empty readme", func(t *testing.T) {
		p := good
		p.Readme = ""
		assertIssue(t, p.Check(existsTrue), "empty")
	})
	t.Run("placeholder readme", func(t *testing.T) {
		p := good
		p.Readme = ReadmeTemplate
		assertIssue(t, p.Check(existsTrue), "template")
	})
	t.Run("tool not worker.action form", func(t *testing.T) {
		p := good
		p.Spec.ToolPermissions = []string{"demo.expensive_generation"}
		p.Spec.Steps[0].Tools = []string{"demo.expensive_generation"}
		issues := p.Check(existsTrue)
		if len(issues) != 2 {
			t.Fatalf("expected form errors for both places, got %v", issues)
		}
	})
	t.Run("unknown worker", func(t *testing.T) {
		p := good
		p.Spec.ToolPermissions = []string{"magic.do_it"}
		assertIssue(t, p.Check(existsTrue), "worker")
	})
	t.Run("eval case missing from registry", func(t *testing.T) {
		p := good
		issues := p.Check(func(string) bool { return false })
		assertIssue(t, issues, "does not exist")
	})
	t.Run("eval suite empty", func(t *testing.T) {
		p := good
		p.Spec.Eval.Suite = ""
		assertIssue(t, p.Check(existsTrue), "suite")
	})
}

func TestStoreRoundtrip(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	pack := Pack{
		Metadata: Metadata{ID: "case-a", Version: "0.1.0", ReviewStatus: ReviewPending},
		Readme:   "text",
	}
	draftPath, err := store.Save(pack, true)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if filepath.Base(filepath.Dir(draftPath)) != "drafts" {
		t.Errorf("draft went to %s, want a drafts/ subdirectory", draftPath)
	}
	loaded, err := LoadPath(draftPath)
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}
	if loaded.Metadata.Status != StatusDraft {
		t.Errorf("draft status = %s", loaded.Metadata.Status)
	}

	pubPath, err := store.Save(loaded, false)
	if err != nil {
		t.Fatalf("save published: %v", err)
	}
	got, err := store.Load("case-a")
	if err != nil {
		t.Fatalf("load published: %v", err)
	}
	if got.Metadata.Status != StatusPublished {
		t.Errorf("published status = %s", got.Metadata.Status)
	}
	if _, err := os.Stat(pubPath); err != nil {
		t.Errorf("published file missing: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Metadata.ID != "case-a" {
		t.Errorf("list = %+v", list)
	}
}

func assertIssue(t *testing.T, issues []string, want string) {
	t.Helper()
	for _, issue := range issues {
		if strings.Contains(issue, want) {
			return
		}
	}
	t.Errorf("expected an issue containing %q, got %v", want, issues)
}
