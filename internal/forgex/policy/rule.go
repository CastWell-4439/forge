package policy

import "strings"

// Config is the top-level policy config.
type Config struct {
	Version int    `json:"version" yaml:"version"`
	Profile string `json:"profile" yaml:"profile"`
	Rules   []Rule `json:"rules" yaml:"rules"`
}

// Rule maps a condition to a policy action.
type Rule struct {
	ID     string    `json:"id" yaml:"id"`
	When   Condition `json:"when" yaml:"when"`
	Action Action    `json:"action" yaml:"action"`
	Reason string    `json:"reason" yaml:"reason"`
}

// Condition supports the first safe-default policy matching surface.
type Condition struct {
	ToolName         string `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	RiskLevel        string `json:"risk_level,omitempty" yaml:"risk_level,omitempty"`
	SideEffect       string `json:"side_effect,omitempty" yaml:"side_effect,omitempty"`
	AuthorityBelow   string `json:"authority_below,omitempty" yaml:"authority_below,omitempty"`
	AuthorityAtLeast string `json:"authority_at_least,omitempty" yaml:"authority_at_least,omitempty"`
	ApprovalRequired *bool  `json:"approval_required,omitempty" yaml:"approval_required,omitempty"`
}

func (c Condition) matches(input decisionInput) bool {
	if c.ToolName != "" && strings.TrimSpace(c.ToolName) != input.ToolName {
		return false
	}
	if c.RiskLevel != "" && strings.ToLower(strings.TrimSpace(c.RiskLevel)) != strings.ToLower(input.RiskLevel) {
		return false
	}
	if c.SideEffect != "" && strings.ToLower(strings.TrimSpace(c.SideEffect)) != strings.ToLower(input.SideEffect) {
		return false
	}
	if c.ApprovalRequired != nil && *c.ApprovalRequired != input.ApprovalRequired {
		return false
	}
	if c.AuthorityBelow != "" {
		threshold := AuthorityLevel(c.AuthorityBelow)
		if !threshold.Valid() || CompareAuthority(input.Authority, threshold) >= 0 {
			return false
		}
	}
	if c.AuthorityAtLeast != "" {
		threshold := AuthorityLevel(c.AuthorityAtLeast)
		if !threshold.Valid() || CompareAuthority(input.Authority, threshold) < 0 {
			return false
		}
	}
	return true
}
