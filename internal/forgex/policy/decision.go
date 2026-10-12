package policy

import (
	"time"

	"github.com/castwell/forge/internal/identity"
)

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
	// Actor is WHO the decision was made about.
	//
	// A denial is only actionable if the reader can see whose request it was:
	// "denied by rule r-7" and "denied by rule r-7, for alice" lead to different
	// next steps, and the second is what an operator needs. The zero value means
	// no identity was declared, which the Actor reports about itself.
	Actor     identity.Actor `json:"actor" yaml:"actor"`
	CreatedAt time.Time      `json:"created_at" yaml:"created_at"`
}
