package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCoreModelsJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 26, 20, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		value any
	}{
		{
			name: "Run",
			value: Run{
				ID:        "run_001",
				TaskID:    "task_001",
				Name:      "AIhook badcase demo",
				Status:    RunRunning,
				StartedAt: now,
				Summary:   "demo",
			},
		},
		{
			name: "TaskPacket",
			value: TaskPacket{
				ID:          "task_001",
				Name:        "AIhook empty images_refs demo",
				Goal:        "Generate an AI Hook video.",
				Inputs:      map[string]any{"material_id": float64(121503)},
				Constraints: []string{"images_refs must be non-empty"},
				Success:     []string{"stop decision is stop"},
				Metadata:    map[string]string{"source": "aihook_real_badcase"},
			},
		},
		{
			name: "Event",
			value: Event{
				ID:        "evt_001",
				RunID:     "run_001",
				Type:      EventToolFailed,
				Message:   "tool failed",
				Timestamp: now,
				Data:      map[string]any{"tool": "vidu.reference2video"},
			},
		},
		{
			name: "Span",
			value: Span{
				ID:        "span_001",
				RunID:     "run_001",
				ParentID:  "span_root",
				Name:      "vidu.reference2video",
				StartedAt: now,
				Status:    "failed",
				Attrs:     map[string]any{"operation": "vidu"},
			},
		},
		{
			name: "ToolCall",
			value: ToolCall{
				ID:        "tool_001",
				RunID:     "run_001",
				ToolName:  "vidu.reference2video",
				Args:      map[string]any{"images_refs": []any{}},
				Error:     "images_refs is empty",
				StartedAt: now,
			},
		},
		{
			name: "ErrorEnvelope",
			value: ErrorEnvelope{
				ID:          "err_001",
				RunID:       "run_001",
				Source:      "tool",
				Operation:   "vidu.reference2video",
				Message:     "images_refs is empty",
				RawError:    "bad request: images_refs is empty",
				Category:    "tool_contract_violation",
				Severity:    "high",
				Fingerprint: "abc123",
				Retryable:   false,
				Metadata:    map[string]string{"rule": "AIHOOK_EMPTY_IMAGES_REFS"},
				Timestamp:   now,
			},
		},
		{
			name: "StopDecision",
			value: StopDecision{
				ID:        "decision_001",
				RunID:     "run_001",
				ErrorID:   "err_001",
				Action:    StopActionStop,
				Reason:    "tool contract violation",
				DecidedAt: now,
			},
		},
		{
			name: "Artifact",
			value: Artifact{
				ID:        "artifact_001",
				RunID:     "run_001",
				Name:      "report",
				Kind:      "markdown",
				Path:      ".forgex/runs/run_001/report.md",
				Metadata:  map[string]string{"format": "md"},
				CreatedAt: now,
			},
		},
		{
			name: "EvalResult",
			value: EvalResult{
				ID:        "eval_001",
				RunID:     "run_001",
				SuiteID:   "aihook_regression_v1",
				Status:    EvalPassed,
				Details:   map[string]string{"assertion": "stop"},
				CreatedAt: now,
			},
		},
		{
			name: "Lesson",
			value: Lesson{
				ID:          "lesson_001",
				Title:       "Do not call Vidu with empty images_refs",
				SourceRunID: "run_001",
				Category:    "tool_contract_violation",
				Content:     "Stop before paid generation when reference images are empty.",
				Metadata:    map[string]string{"source": "real_badcase"},
				CreatedAt:   now,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if len(encoded) == 0 {
				t.Fatalf("marshal %s produced empty JSON", tc.name)
			}

			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			if len(decoded) == 0 {
				t.Fatalf("unmarshal %s produced empty object", tc.name)
			}
		})
	}
}

func TestEnumValues(t *testing.T) {
	assertions := map[string]string{
		"RunPending":         string(RunPending),
		"RunRunning":         string(RunRunning),
		"RunSucceeded":       string(RunSucceeded),
		"RunFailed":          string(RunFailed),
		"RunStopped":         string(RunStopped),
		"RunEscalated":       string(RunEscalated),
		"EventRunStarted":    string(EventRunStarted),
		"EventToolFailed":    string(EventToolFailed),
		"StopActionContinue": string(StopActionContinue),
		"StopActionRetry":    string(StopActionRetry),
		"StopActionStop":     string(StopActionStop),
		"StopActionEscalate": string(StopActionEscalate),
		"EvalPassed":         string(EvalPassed),
		"EvalFailed":         string(EvalFailed),
		"EvalSkipped":        string(EvalSkipped),
	}

	for name, value := range assertions {
		if value == "" {
			t.Fatalf("%s must not be empty", name)
		}
	}
}
