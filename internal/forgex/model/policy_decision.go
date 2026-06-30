package model

import "time"

// PolicyDecision records one control-plane authorization decision for a tool invocation.
type PolicyDecision struct {
	ID           string    `json:"id" yaml:"id"`
	RunID        string    `json:"run_id" yaml:"run_id"`
	ToolName     string    `json:"tool_name" yaml:"tool_name"`
	Action       string    `json:"action" yaml:"action"`
	Reason       string    `json:"reason" yaml:"reason"`
	RiskLevel    string    `json:"risk_level" yaml:"risk_level"`
	SideEffect   string    `json:"side_effect" yaml:"side_effect"`
	Authority    string    `json:"authority" yaml:"authority"`
	RequiresHITL bool      `json:"requires_hitl" yaml:"requires_hitl"`
	CreatedAt    time.Time `json:"created_at" yaml:"created_at"`
}
