package task

import (
	"strconv"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
)

// SuitabilityDecision is the rule-gate outcome before a harness run starts.
type SuitabilityDecision string

const (
	SuitabilitySimple           SuitabilityDecision = "simple"
	SuitabilityAgentWithHarness SuitabilityDecision = "agent_with_harness"
	SuitabilityHumanReview      SuitabilityDecision = "human_review"
)

// SuitabilityInput is the compact signal set described by ForgeX design docs.
type SuitabilityInput struct {
	Ambiguity             string `json:"ambiguity" yaml:"ambiguity"`
	RuleComplexity        string `json:"rule_complexity" yaml:"rule_complexity"`
	ToolRisk              string `json:"tool_risk" yaml:"tool_risk"`
	Reversibility         string `json:"reversibility" yaml:"reversibility"`
	DataSensitivity       string `json:"data_sensitivity" yaml:"data_sensitivity"`
	ExpectedSteps         int    `json:"expected_steps" yaml:"expected_steps"`
	HumanJudgmentRequired bool   `json:"human_judgment_required" yaml:"human_judgment_required"`
}

// SuitabilityResult records the gate's decision and required controls.
type SuitabilityResult struct {
	Decision         SuitabilityDecision `json:"decision" yaml:"decision"`
	RequiredControls []string            `json:"required_controls,omitempty" yaml:"required_controls,omitempty"`
	Reasons          []string            `json:"reasons,omitempty" yaml:"reasons,omitempty"`
	Score            int                 `json:"score" yaml:"score"`
}

// EvaluateSuitability applies the minimal rule-based AgentSuitabilityGate.
func EvaluateSuitability(input SuitabilityInput) SuitabilityResult {
	result := SuitabilityResult{Decision: SuitabilitySimple}
	add := func(points int, reason string, controls ...string) {
		result.Score += points
		result.Reasons = append(result.Reasons, reason)
		result.RequiredControls = appendUnique(result.RequiredControls, controls...)
	}

	if isHigh(input.Ambiguity) {
		add(2, "high ambiguity", "persistence", "stop_conditions")
	}
	if isMediumOrHigh(input.RuleComplexity) {
		add(1, "non-trivial rule complexity", "persistence")
	}
	if isHigh(input.ToolRisk) {
		add(3, "high tool risk", "approval", "simulation", "stop_conditions")
	}
	if strings.EqualFold(strings.TrimSpace(input.Reversibility), "irreversible") {
		add(3, "irreversible action", "approval", "simulation", "stop_conditions")
	}
	if isHigh(input.DataSensitivity) {
		add(2, "high data sensitivity", "approval", "persistence")
	}
	if input.ExpectedSteps >= 5 {
		add(1, "multi-step task", "persistence")
	}
	if input.HumanJudgmentRequired {
		add(2, "human judgment required", "approval", "stop_conditions")
	}

	switch {
	case strings.EqualFold(strings.TrimSpace(input.ToolRisk), "critical") || strings.EqualFold(strings.TrimSpace(input.DataSensitivity), "critical"):
		result.Decision = SuitabilityHumanReview
	case result.Score >= 3:
		result.Decision = SuitabilityAgentWithHarness
	default:
		result.Decision = SuitabilitySimple
	}
	return result
}

// EvaluatePacket derives suitability signals from a TaskPacket's explicit metadata
// and constraints. It stays conservative: risky or multi-step packets get harness controls.
func EvaluatePacket(packet model.TaskPacket) SuitabilityResult {
	input := SuitabilityInput{
		Ambiguity:             meta(packet, "ambiguity", "medium"),
		RuleComplexity:        meta(packet, "rule_complexity", "medium"),
		ToolRisk:              meta(packet, "tool_risk", inferToolRisk(packet)),
		Reversibility:         meta(packet, "reversibility", "reversible"),
		DataSensitivity:       meta(packet, "data_sensitivity", "medium"),
		ExpectedSteps:         parseInt(meta(packet, "expected_steps", "0")),
		HumanJudgmentRequired: parseBool(meta(packet, "human_judgment_required", "false")),
	}
	if input.ExpectedSteps == 0 && len(packet.Success)+len(packet.Constraints) >= 4 {
		input.ExpectedSteps = len(packet.Success) + len(packet.Constraints)
	}
	return EvaluateSuitability(input)
}

func inferToolRisk(packet model.TaskPacket) string {
	joined := strings.ToLower(packet.Goal + " " + strings.Join(packet.Constraints, " "))
	for _, keyword := range []string{"external", "write", "approval", "paid", "计费", "真实", "api"} {
		if strings.Contains(joined, keyword) {
			return "high"
		}
	}
	return "medium"
}

func meta(packet model.TaskPacket, key, fallback string) string {
	if packet.Metadata == nil || strings.TrimSpace(packet.Metadata[key]) == "" {
		return fallback
	}
	return packet.Metadata[key]
}

func isHigh(level string) bool {
	return strings.EqualFold(strings.TrimSpace(level), "high")
}

func isMediumOrHigh(level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	return level == "medium" || level == "high"
}

func parseInt(value string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(value))
	return v
}

func parseBool(value string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(value))
	return v
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		values = append(values, value)
		seen[value] = true
	}
	return values
}
