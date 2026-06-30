package policy

import "time"

// Action is the policy outcome for a tool invocation.
type Action string

const (
	ActionAllow           Action = "allow"
	ActionDeny            Action = "deny"
	ActionRequireApproval Action = "require_approval"
	ActionDryRunOnly      Action = "dry_run_only"
	ActionPause           Action = "pause"
	ActionEscalate        Action = "escalate"
)

// Decision records one policy decision for a tool invocation.
type Decision struct {
	ID           string         `json:"id" yaml:"id"`
	RunID        string         `json:"run_id" yaml:"run_id"`
	ToolName     string         `json:"tool_name" yaml:"tool_name"`
	Action       Action         `json:"action" yaml:"action"`
	Reason       string         `json:"reason" yaml:"reason"`
	RiskLevel    string         `json:"risk_level" yaml:"risk_level"`
	SideEffect   string         `json:"side_effect" yaml:"side_effect"`
	Authority    AuthorityLevel `json:"authority" yaml:"authority"`
	RequiresHITL bool           `json:"requires_hitl" yaml:"requires_hitl"`
	CreatedAt    time.Time      `json:"created_at" yaml:"created_at"`
}
