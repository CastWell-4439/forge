package report

import (
	"strings"
	"testing"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// genericSnapshot builds a snapshot resembling the empty required_assets run.
func genericSnapshot() RunSnapshot {
	ts := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	return RunSnapshot{
		Run: model.Run{
			ID:        "run_generic_001",
			TaskID:    "generic_contract_violation_demo",
			Name:      "Generic contract violation demo",
			Status:    model.RunStopped,
			StartedAt: ts,
		},
		TaskPacket: model.TaskPacket{
			ID:   "generic_contract_violation_demo",
			Name: "Generic contract violation demo",
			Goal: "Run demo.expensive_generation with required assets.",
			Inputs: map[string]any{
				"request_id": "demo-req-001",
			},
			Metadata: map[string]string{"source": "generic_contract_violation"},
		},
		Events: []model.Event{
			{Type: model.EventRunStarted, Message: "run started", Timestamp: ts},
			{Type: model.EventToolCalled, Message: "tool called: demo.expensive_generation", Timestamp: ts.Add(time.Second)},
			{Type: model.EventStopDecided, Message: "stop decision: stop", Timestamp: ts.Add(2 * time.Second)},
		},
		ToolCalls: []model.ToolCall{
			{
				ToolName:  "demo.expensive_generation",
				Args:      map[string]any{"required_assets": []any{}},
				Error:     "required_assets is empty",
				StartedAt: ts.Add(time.Second),
			},
		},
		Errors: []model.ErrorEnvelope{
			{
				ID:          "err_1",
				Source:      "tool",
				Operation:   "demo.expensive_generation",
				Message:     "required_assets is empty",
				Category:    "tool_contract_violation",
				Severity:    "high",
				Retryable:   false,
				Fingerprint: "abcd1234abcd1234",
				Metadata: map[string]string{
					"rule_id":        "GENERIC_REQUIRED_ASSETS_EMPTY",
					"source":         "demo",
					"recommendation": "demo.expensive_generation must provide non-empty required_assets.",
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
	md := GenerateMarkdown(genericSnapshot())

	wants := []string{
		"# ForgeX Run Report",
		"## Summary",
		"## Control Metrics",
		"## Task Packet",
		"## Progress Ledger",
		"## Context Packs",
		"## Timeline",
		"## Tool Calls",
		"## Errors",
		"## Stop Decisions",
		"## Suggested Fix",
		"run_generic_001",
		"Run demo.expensive_generation with required assets.",
		"demo.expensive_generation",
		"required_assets is empty",
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

func TestGenerateMarkdownControlMetrics(t *testing.T) {
	snapshot := genericSnapshot()
	snapshot.PolicyDecisions = []model.PolicyDecision{
		{ToolName: "demo.expensive_generation", Action: "require_approval", RequiresHITL: true},
	}
	snapshot.ContractValidations = []model.ContractValidation{
		{ToolName: "demo.expensive_generation", Status: "failed", Message: "required_assets is empty"},
	}
	snapshot.Artifacts = []model.ArtifactRecord{
		{ID: "art_1", Type: "required_asset", Status: model.ArtifactMissing},
	}

	md := GenerateMarkdown(snapshot)

	wants := []string{
		"## Control Metrics",
		"- **Policy Decisions**: 1",
		"- **Approval Required**: 1",
		"- **Contract Validation Failed**: 1",
		"- **Safe Stop**: 1",
		"- **Missing Artifacts**: 1",
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
	out, err := GenerateBadCaseYAML(genericSnapshot())
	if err != nil {
		t.Fatalf("GenerateBadCaseYAML: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("bad case yaml is not parseable: %v\n%s", err, out)
	}

	if parsed["run_id"] != "run_generic_001" {
		t.Errorf("run_id = %v, want run_generic_001", parsed["run_id"])
	}
	if parsed["failure_category"] != "tool_contract_violation" {
		t.Errorf("failure_category = %v, want tool_contract_violation", parsed["failure_category"])
	}
	if parsed["expected_decision"] != "stop" {
		t.Errorf("expected_decision = %v, want stop", parsed["expected_decision"])
	}
	if parsed["id"] != "GENERIC_REQUIRED_ASSETS_EMPTY" {
		t.Errorf("id = %v, want GENERIC_REQUIRED_ASSETS_EMPTY", parsed["id"])
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
