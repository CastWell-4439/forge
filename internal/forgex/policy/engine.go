package policy

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/castwell/forge/internal/forgex/toolgw"
	"gopkg.in/yaml.v3"
)

var decisionSeq atomic.Uint64

// Engine evaluates tool contracts against policy rules and safe defaults.
type Engine struct {
	cfg *Config
	now func() time.Time
}

type decisionInput struct {
	ToolName         string
	RiskLevel        string
	SideEffect       string
	ApprovalRequired bool
	Authority        AuthorityLevel
}

// LoadConfig reads a policy YAML file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse policy config %s: %w", path, err)
	}
	if cfg.Version <= 0 {
		return nil, fmt.Errorf("policy config %s: version is required", path)
	}
	return &cfg, nil
}

// NewEngine creates a policy engine. Nil config means no rules, safe defaults only.
func NewEngine(cfg *Config) *Engine {
	if cfg == nil {
		cfg = &Config{}
	}
	return &Engine{cfg: cfg, now: time.Now}
}

// Decide returns one policy decision for a tool contract and authority level.
func (e *Engine) Decide(runID string, authority AuthorityLevel, contract toolgw.ToolContract) Decision {
	if e == nil {
		e = NewEngine(nil)
	}
	authority = NormalizeAuthority(authority)
	input := decisionInput{
		ToolName:         strings.TrimSpace(contract.Name),
		RiskLevel:        strings.ToLower(strings.TrimSpace(string(contract.RiskLevel))),
		SideEffect:       strings.ToLower(strings.TrimSpace(string(contract.SideEffect))),
		ApprovalRequired: contract.ApprovalRequired,
		Authority:        authority,
	}
	decision := Decision{
		ID:           newDecisionID(runID),
		RunID:        runID,
		ToolName:     contract.Name,
		RiskLevel:    string(contract.RiskLevel),
		SideEffect:   string(contract.SideEffect),
		Authority:    authority,
		RequiresHITL: false,
		CreatedAt:    e.now().UTC(),
	}

	for _, rule := range e.cfg.Rules {
		if rule.When.matches(input) {
			decision.Action = rule.Action
			decision.Reason = ruleReason(rule)
			decision.RequiresHITL = rule.Action == ActionRequireApproval || rule.Action == ActionPause || rule.Action == ActionEscalate
			return decision
		}
	}

	applySafeDefault(&decision, authority, contract)
	return decision
}

func applySafeDefault(decision *Decision, authority AuthorityLevel, contract toolgw.ToolContract) {
	required := AuthorityLevel(contract.RequiredAuthorityLevel)
	if strings.TrimSpace(string(required)) != "" && required.Valid() && CompareAuthority(authority, required) < 0 {
		decision.Action = ActionDeny
		decision.Reason = fmt.Sprintf("authority %s is below required %s", authority, NormalizeAuthority(required))
		return
	}
	if contract.ApprovalRequired {
		decision.Action = ActionRequireApproval
		decision.Reason = "tool contract requires approval"
		decision.RequiresHITL = true
		return
	}
	if contract.SideEffect == toolgw.SideEffectExternalAPICall && CompareAuthority(authority, AuthorityL2) < 0 {
		decision.Action = ActionDeny
		decision.Reason = fmt.Sprintf("%s side effect requires authority %s or above", contract.SideEffect, AuthorityL2)
		return
	}
	if contract.RiskLevel == toolgw.RiskHigh || contract.RiskLevel == toolgw.RiskCritical {
		decision.Action = ActionRequireApproval
		decision.Reason = fmt.Sprintf("%s risk tool requires approval by safe default", contract.RiskLevel)
		decision.RequiresHITL = true
		return
	}
	if (contract.SideEffect == toolgw.SideEffectPaidCall || contract.SideEffect == toolgw.SideEffectExternalWrite) && CompareAuthority(authority, AuthorityL3) < 0 {
		decision.Action = ActionDeny
		decision.Reason = fmt.Sprintf("%s side effect requires authority %s or above", contract.SideEffect, AuthorityL3)
		return
	}
	decision.Action = ActionAllow
	decision.Reason = "safe default allow"
}

func ruleReason(rule Rule) string {
	if strings.TrimSpace(rule.Reason) != "" {
		return rule.Reason
	}
	if strings.TrimSpace(rule.ID) != "" {
		return "matched policy " + rule.ID
	}
	return "matched unnamed policy"
}

func newDecisionID(runID string) string {
	seq := decisionSeq.Add(1)
	if strings.TrimSpace(runID) == "" {
		runID = "run"
	}
	return fmt.Sprintf("policy-%s-%d", runID, seq)
}
