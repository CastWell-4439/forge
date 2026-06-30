// Package stop implements the ForgeX M5 StopConditionEngine: it turns a
// classified ErrorEnvelope into a StopDecision (continue/retry/stop/escalate)
// using an ordered policy list plus a per-fingerprint retry budget.
package stop

import (
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
)

// PolicyConfig is the YAML stop-policy configuration loaded from
// configs/forgex/stop_policies.yaml.
type PolicyConfig struct {
	Version     int               `json:"version" yaml:"version"`
	RetryBudget RetryBudgetConfig `json:"retry_budget" yaml:"retry_budget"`
	Policies    []StopPolicy      `json:"policies" yaml:"policies"`
}

// RetryBudgetConfig caps how many retries a single error fingerprint may consume.
// ByCategory overrides DefaultMaxRetries for specific failure categories.
type RetryBudgetConfig struct {
	DefaultMaxRetries int            `json:"default_max_retries" yaml:"default_max_retries"`
	ByCategory        map[string]int `json:"by_category,omitempty" yaml:"by_category,omitempty"`
}

// budgetFor returns the retry budget for a category, falling back to the default
// when the category has no explicit override.
func (c RetryBudgetConfig) budgetFor(category string) int {
	if c.ByCategory != nil {
		if v, ok := c.ByCategory[category]; ok {
			return v
		}
	}
	return c.DefaultMaxRetries
}

// StopPolicy is a single ordered rule. The first policy whose When matches an
// envelope wins; its Action is mapped to a model.StopAction.
type StopPolicy struct {
	ID     string     `json:"id" yaml:"id"`
	When   PolicyWhen `json:"when" yaml:"when"`
	Action string     `json:"action" yaml:"action"`
	Reason string     `json:"reason" yaml:"reason"`
}

// StopAction maps the policy's configured action string to a model.StopAction.
func (p StopPolicy) StopAction() model.StopAction {
	return parseAction(p.Action)
}

// PolicyWhen declares the match conditions for a policy. Matching is
// case-insensitive and an empty field is treated as a wildcard.
type PolicyWhen struct {
	Category string `json:"category,omitempty" yaml:"category,omitempty"`
	Severity string `json:"severity,omitempty" yaml:"severity,omitempty"`
}

// matches reports whether the (already lower-cased) category/severity satisfy
// this condition. Empty condition fields match anything.
func (w PolicyWhen) matches(category, severity string) bool {
	if wc := strings.ToLower(strings.TrimSpace(w.Category)); wc != "" && wc != category {
		return false
	}
	if ws := strings.ToLower(strings.TrimSpace(w.Severity)); ws != "" && ws != severity {
		return false
	}
	return true
}

// parseAction maps a configured action string to a model.StopAction. Unknown or
// empty values fall back to StopActionStop so a malformed policy fails safe by
// halting rather than silently continuing.
func parseAction(action string) model.StopAction {
	switch model.StopAction(strings.ToLower(strings.TrimSpace(action))) {
	case model.StopActionContinue:
		return model.StopActionContinue
	case model.StopActionRetry:
		return model.StopActionRetry
	case model.StopActionStop:
		return model.StopActionStop
	case model.StopActionEscalate:
		return model.StopActionEscalate
	case model.StopActionPause:
		return model.StopActionPause
	default:
		return model.StopActionStop
	}
}
