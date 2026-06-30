package task

import (
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func TestEvaluateSuitabilityAgentWithHarness(t *testing.T) {
	result := EvaluateSuitability(SuitabilityInput{
		Ambiguity:             "high",
		RuleComplexity:        "medium",
		ToolRisk:              "high",
		Reversibility:         "reversible",
		DataSensitivity:       "medium",
		ExpectedSteps:         12,
		HumanJudgmentRequired: true,
	})
	if result.Decision != SuitabilityAgentWithHarness {
		t.Fatalf("Decision = %s, want %s", result.Decision, SuitabilityAgentWithHarness)
	}
	for _, want := range []string{"approval", "simulation", "stop_conditions", "persistence"} {
		if !contains(result.RequiredControls, want) {
			t.Fatalf("RequiredControls missing %q: %+v", want, result.RequiredControls)
		}
	}
}

func TestEvaluateSuitabilitySimple(t *testing.T) {
	result := EvaluateSuitability(SuitabilityInput{ToolRisk: "low", DataSensitivity: "low", ExpectedSteps: 1})
	if result.Decision != SuitabilitySimple {
		t.Fatalf("Decision = %s, want %s", result.Decision, SuitabilitySimple)
	}
}

func TestEvaluatePacketInfersHarnessForRiskyExternalAPITask(t *testing.T) {
	result := EvaluatePacket(model.TaskPacket{
		ID:   "task_1",
		Goal: "Call external paid API in dry-run mode",
		Constraints: []string{
			"计费操作必须人工确认",
			"真实 API 写操作禁止自动执行",
		},
		Success: []string{"stop conditions catch critical failures", "report generated"},
	})
	if result.Decision != SuitabilityAgentWithHarness {
		t.Fatalf("Decision = %s, want %s", result.Decision, SuitabilityAgentWithHarness)
	}
	if !contains(result.RequiredControls, "approval") || !contains(result.RequiredControls, "stop_conditions") {
		t.Fatalf("controls missing approval/stop_conditions: %+v", result.RequiredControls)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
