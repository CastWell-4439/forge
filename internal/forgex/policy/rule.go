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

// Condition supports the safe-default policy matching surface.
type Condition struct {
	ToolName         string `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	RiskLevel        string `json:"risk_level,omitempty" yaml:"risk_level,omitempty"`
	SideEffect       string `json:"side_effect,omitempty" yaml:"side_effect,omitempty"`
	AuthorityBelow   string `json:"authority_below,omitempty" yaml:"authority_below,omitempty"`
	AuthorityAtLeast string `json:"authority_at_least,omitempty" yaml:"authority_at_least,omitempty"`
	ApprovalRequired *bool  `json:"approval_required,omitempty" yaml:"approval_required,omitempty"`

	// The WHO dimension, orthogonal to everything above.
	//
	// Every other field asks what is being done — which tool, how risky, with
	// what side effect, under how much authority. None asks who is asking. That
	// gap is why a deployment could say "deleting is allowed at L4" and had no
	// way to say "deleting is allowed at L4, but not by the CI runner".
	//
	// Actor and Authority are separate dimensions rather than one scale.
	// Authority is what a RUN was granted; Actor is who is asking. Folding them
	// together would conflate "this run may act autonomously" with "this person
	// may act destructively", and a long-running L4 run is exactly where that
	// confusion costs the most.
	Actor string `json:"actor,omitempty" yaml:"actor,omitempty"`
	// ActorDeclared matches on whether the caller declared an identity at all.
	//
	// A pointer, so "not specified" is distinguishable from "must be
	// undeclared" — the same three-state rule the other optional fields follow.
	//
	// It exists so a deployment can choose the stricter posture WITHOUT a code
	// change: a rule with `actor_declared: false → deny` turns every
	// unattributed call away. Whether that is the right default is a policy
	// question, which is why the engine does not answer it.
	ActorDeclared *bool `json:"actor_declared,omitempty" yaml:"actor_declared,omitempty"`
	// ActorVerified matches on whether the identity was CHECKED.
	//
	// Nothing verifies an identity today, so a rule using this can only match
	// false. That is the honest behaviour rather than a limitation to work
	// around: a rule requiring a verified actor matches NOTHING, instead of
	// matching a claim that was never checked. Writing such a rule now is
	// therefore safe — it takes effect the day verification exists, and does
	// nothing before then.
	ActorVerified *bool `json:"actor_verified,omitempty" yaml:"actor_verified,omitempty"`
}

func (c Condition) matches(input decisionInput) bool {
	if c.ToolName != "" && strings.TrimSpace(c.ToolName) != input.ToolName {
		return false
	}
	if c.RiskLevel != "" && !strings.EqualFold(strings.TrimSpace(c.RiskLevel), input.RiskLevel) {
		return false
	}
	if c.SideEffect != "" && !strings.EqualFold(strings.TrimSpace(c.SideEffect), input.SideEffect) {
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

	// Actor matching is case-insensitive: an identity arrives from a header or
	// metadata field written by an operator, so "Alice" versus "alice" is a typo
	// rather than a different principal. A rule that silently failed to match on
	// case would be a permission that quietly stopped applying.
	if c.Actor != "" && !strings.EqualFold(strings.TrimSpace(c.Actor), input.Actor) {
		return false
	}
	if c.ActorDeclared != nil && *c.ActorDeclared != input.ActorDeclared {
		return false
	}
	if c.ActorVerified != nil && *c.ActorVerified != input.ActorVerified {
		return false
	}
	return true
}
