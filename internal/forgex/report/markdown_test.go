package report

import (
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// aihookSnapshot builds a snapshot resembling the AIhook empty images_refs run.
func aihookSnapshot() RunSnapshot {
	ts := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	return RunSnapshot{
		Run: model.Run{
			ID:        "run_aihook_001",
			TaskID:    "aihook_empty_images_refs_demo",
			Name:      "AIhook empty images_refs demo",
			Status:    model.RunStopped,
			StartedAt: ts,
		},
		TaskPacket: model.TaskPacket{
			ID:   "aihook_empty_images_refs_demo",
			Name: "AIhook empty images_refs demo",
			Goal: "Generate AI Hook video with Vidu reference2video.",
			Inputs: map[string]any{
				"material_id": 121503,
			},
			Metadata: map[string]string{"source": "aihook_real_badcase"},
		},
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: ts},
			{Type: model.EventToolCalled, Message: "tool called: vidu.reference2video", Timestamp: ts.Add(time.Second)},
			{Type: model.EventStopDecided, Message: "stop decision: stop", Timestamp: ts.Add(2 * time.Second)},
		},
		ToolCalls: []model.ToolCall{
			{
				ToolName:  "vidu.reference2video",
				Args:      map[string]any{"images_refs": []any{}},
				Error:     "images_refs is empty",
				StartedAt: ts.Add(time.Second),
			},
		},
		Errors: []model.ErrorEnvelope{
			{
				ID:          "err_1",
				Source:      "tool",
				Operation:   "vidu.reference2video",
				Message:     "images_refs is empty",
				Category:    "tool_contract_violation",
				Severity:    "high",
				Retryable:   false,
				Fingerprint: "abcd1234abcd1234",
				Metadata: map[string]string{
					"rule_id":        "AIHOOK_EMPTY_IMAGES_REFS",
					"source":         "real_badcase",
					"recommendation": "Vidu reference2video must provide non-empty images_refs.",
				},
			},
		},
		StopDecisions: []model.StopDecision{
			{
				ID:      "dec_1",
				ErrorID: "err_1",
				Action:  model.StopActionStop,
				Reason:  "Tool contract violation is not retryable without changing inputs.",
			},
		},
	}
}

func TestGenerateMarkdownContainsKeyFields(t *testing.T) {
	md := GenerateMarkdown(aihookSnapshot())

	wants := []string{
		"# ForgeX Run Report",
		"## Summary",
		"## Timeline",
		"## Tool Calls",
		"## Errors",
		"## Stop Decisions",
		"## Suggested Fix",
		"run_aihook_001",
		"Generate AI Hook video with Vidu reference2video.",
		"vidu.reference2video",
		"images_refs is empty",
		"tool_contract_violation",
		"high",
		"Retryable: false",
		"stop",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("report missing %q\n---\n%s", w, md)
		}
	}
}

func TestGenerateMarkdownEmptyErrorsStillRenders(t *testing.T) {
	snapshot := RunSnapshot{
		Run: model.Run{ID: "run_empty", Status: model.RunSucceeded},
		TaskPacket: model.TaskPacket{
			Name: "empty run",
			Goal: "do nothing",
		},
	}
	md := GenerateMarkdown(snapshot)

	for _, section := range []string{"## Summary", "## Timeline", "## Errors", "## Stop Decisions", "## Suggested Fix"} {
		if !strings.Contains(md, section) {
			t.Errorf("report missing section %q", section)
		}
	}
	if !strings.Contains(md, "run_empty") {
		t.Errorf("report missing run id\n%s", md)
	}
	if !strings.Contains(md, "_No errors recorded._") {
		t.Errorf("report missing empty-errors placeholder\n%s", md)
	}
}

func TestGenerateBadCaseYAMLParseable(t *testing.T) {
	out, err := GenerateBadCaseYAML(aihookSnapshot())
	if err != nil {
		t.Fatalf("GenerateBadCaseYAML: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("bad case yaml is not parseable: %v\n%s", err, out)
	}

	if parsed["run_id"] != "run_aihook_001" {
		t.Errorf("run_id = %v, want run_aihook_001", parsed["run_id"])
	}
	if parsed["failure_category"] != "tool_contract_violation" {
		t.Errorf("failure_category = %v, want tool_contract_violation", parsed["failure_category"])
	}
	if parsed["expected_decision"] != "stop" {
		t.Errorf("expected_decision = %v, want stop", parsed["expected_decision"])
	}
	if parsed["id"] != "AIHOOK_EMPTY_IMAGES_REFS" {
		t.Errorf("id = %v, want AIHOOK_EMPTY_IMAGES_REFS", parsed["id"])
	}

	replay, ok := parsed["replay"].(map[string]any)
	if !ok {
		t.Fatalf("replay is not a map: %T", parsed["replay"])
	}
	if _, ok := replay["task_packet"]; !ok {
		t.Errorf("replay missing task_packet")
	}

	assertions, ok := parsed["assertions"].([]any)
	if !ok || len(assertions) == 0 {
		t.Fatalf("assertions missing or empty: %v", parsed["assertions"])
	}
}

func TestGenerateBadCaseYAMLEmptyErrors(t *testing.T) {
	snapshot := RunSnapshot{
		Run:        model.Run{ID: "run_empty"},
		TaskPacket: model.TaskPacket{Name: "empty run"},
	}
	out, err := GenerateBadCaseYAML(snapshot)
	if err != nil {
		t.Fatalf("GenerateBadCaseYAML: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("empty bad case yaml is not parseable: %v\n%s", err, out)
	}
	if parsed["id"] != "FORGEX_BADCASE" {
		t.Errorf("id = %v, want FORGEX_BADCASE", parsed["id"])
	}
}
