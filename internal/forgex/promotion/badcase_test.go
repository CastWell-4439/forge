package promotion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

func TestPromoteWritesReviewDraft(t *testing.T) {
	runDir := t.TempDir()
	writeJSON(t, filepath.Join(runDir, "run.json"), model.Run{ID: "run_1", Status: model.RunStopped, StartedAt: time.Now().UTC()})
	writeYAML(t, filepath.Join(runDir, "task_packet.yaml"), model.TaskPacket{ID: "task_1", Name: "Task", Goal: "Goal"})
	writeJSONL(t, filepath.Join(runDir, "errors.jsonl"), model.ErrorEnvelope{Category: "tool_contract_violation"})
	writeJSONL(t, filepath.Join(runDir, "stop_decisions.jsonl"), model.StopDecision{Action: model.StopActionStop})
	writeFile(t, filepath.Join(runDir, "lessons.jsonl"), []byte(`{"id":"lesson_1"}`+"\n"))
	writeFile(t, filepath.Join(runDir, "badcase.yaml"), []byte(`id: FORGEX_BADCASE_TOOL_CONTRACT
`+`title: Contract violation
`+`run_id: run_1
`+`failure_category: tool_contract_violation
`+`expected_decision: stop
`))

	out := filepath.Join(t.TempDir(), "draft.yaml")
	draft, err := Promote(runDir, out)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if draft.ID != "forgex_badcase_tool_contract" {
		t.Fatalf("draft id = %q", draft.ID)
	}
	if !draft.ReviewRequired || draft.ReviewStatus != "pending" {
		t.Fatalf("review fields = %+v", draft)
	}
	if draft.Expected.FinalDecision != "stop" || draft.Expected.Status != string(model.RunStopped) {
		t.Fatalf("expected = %+v", draft.Expected)
	}
	if draft.Expected.LessonsMin != 1 {
		t.Fatalf("lessons_min = %d", draft.Expected.LessonsMin)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if !strings.Contains(string(data), "review_required: true") {
		t.Fatalf("draft missing review marker:\n%s", string(data))
	}
}

func TestPromoteRequiresBadcase(t *testing.T) {
	_, err := Promote(t.TempDir(), filepath.Join(t.TempDir(), "draft.yaml"))
	if err == nil {
		t.Fatalf("Promote() expected error")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	writeFile(t, path, append(data, '\n'))
}

func writeYAML(t *testing.T, path string, value any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	writeFile(t, path, data)
}

func writeJSONL(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal jsonl: %v", err)
	}
	writeFile(t, path, append(data, '\n'))
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
