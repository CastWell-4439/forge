package policy

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/identity"
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
	// The WHO dimension: the declared principal, whether one was declared at
	// all, and whether it was checked. All three travel together because a
	// rule may constrain any of them.
	Actor         string
	ActorDeclared bool
	ActorVerified bool
	// ActorRoles is the set of roles the caller belongs to, already normalised.
	// KnownRoles is the set of roles the policy defines.
	//
	// They are resolved once by the engine rather than looked up per rule: every
	// rule asks the same question, and resolving it N times would make the
	// membership test depend on the config being read repeatedly.
	ActorRoles map[string]bool
	KnownRoles map[string]bool
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
//
// It is the historical entry point and is kept so every existing caller keeps
// working. It decides as an UNATTRIBUTED caller, which is what those callers
// are: they pass no identity, so rules that constrain one do not apply to them.
// A deployment that wants unattributed calls refused says so in policy (see
// Condition.ActorDeclared), not by this function changing behaviour underneath
// its existing users.
func (e *Engine) Decide(runID string, authority AuthorityLevel, contract toolgw.ToolContract) Decision {
	return e.DecideFor(runID, authority, contract, identity.Actor{})
}

// DecideFor returns one policy decision for a caller with a declared identity.
//
// The actor is a PARAMETER rather than read from a context, because this engine
// is otherwise a pure function of its inputs and its tests rely on that: a value
// that arrives through a context makes every rule's behaviour depend on
// something the call site does not show.
//
// Actor and authority are independent, and both must pass. A run granted L4
// authority still faces rules written against the caller, and a rule requiring
// an actor does not care what authority the run was granted.
func (e *Engine) DecideFor(runID string, authority AuthorityLevel, contract toolgw.ToolContract, actor identity.Actor) Decision {
	if e == nil {
		e = NewEngine(nil)
	}
	authority = NormalizeAuthority(authority)
	actorID, actorDeclared := actor.Declared()

	input := decisionInput{
		ToolName:         strings.TrimSpace(contract.Name),
		RiskLevel:        strings.ToLower(strings.TrimSpace(string(contract.RiskLevel))),
		SideEffect:       strings.ToLower(strings.TrimSpace(string(contract.SideEffect))),
		ApprovalRequired: contract.ApprovalRequired,
		Authority:        authority,
		Actor:            actorID,
		ActorDeclared:    actorDeclared,
		// Read from the Actor rather than assumed: today it is always false, and
		// taking it from the value means the day verification exists, rules using
		// it start working without an edit here.
		ActorVerified: actor.Verified(),
		KnownRoles:    e.knownRoles(),
		ActorRoles:    e.rolesOf(actorID, actorDeclared),
	}
	decision := Decision{
		ID:           newDecisionID(runID),
		RunID:        runID,
		ToolName:     contract.Name,
		RiskLevel:    string(contract.RiskLevel),
		SideEffect:   string(contract.SideEffect),
		Authority:    authority,
		RequiresHITL: false,
		Actor:        actor,
		CreatedAt:    e.now().UTC(),
	}

	// Undefined roles referenced by rules are collected while scanning, so the
	// final reason can name them.
	//
	// This is what keeps "an undefined role matches nothing" from being a silent
	// failure. The rule that references one can never fire — and a permission
	// that quietly stopped applying is the expensive kind of mistake — so the
	// decision says which role was undefined. A reader at the point of refusal
	// then learns exactly what a startup error would have told them, without
	// paying the wider cost of refusing to load the whole policy over one typo.
	var undefinedRoles []string
	seenUndefined := map[string]bool{}

	for _, rule := range e.cfg.Rules {
		if rule.When.ActorRole != "" {
			role := normalizeKey(rule.When.ActorRole)
			if !input.KnownRoles[role] && !seenUndefined[role] {
				seenUndefined[role] = true
				undefinedRoles = append(undefinedRoles, rule.When.ActorRole)
			}
		}
		if rule.When.matches(input) {
			decision.Action = rule.Action
			decision.Reason = ruleReason(rule)
			decision.RequiresHITL = rule.Action == ActionRequireApproval || rule.Action == ActionPause || rule.Action == ActionEscalate
			return decision
		}
	}

	applySafeDefault(&decision, authority, contract)

	// The note is appended to whatever the outcome was, including a safe-default
	// denial or allow: the undefined role is a fact about the POLICY, not about
	// this decision, and a reader needs it whichever way the decision went.
	if len(undefinedRoles) > 0 {
		sort.Strings(undefinedRoles)
		note := fmt.Sprintf("policy references undefined role(s): %s", strings.Join(undefinedRoles, ", "))
		if decision.Reason == "" {
			decision.Reason = note
		} else {
			decision.Reason = decision.Reason + "; " + note
		}
	}
	return decision
}

// knownRoles is the set of role names the policy defines, normalised.
func (e *Engine) knownRoles() map[string]bool {
	out := make(map[string]bool, len(e.cfg.Roles))
	for name := range e.cfg.Roles {
		out[normalizeKey(name)] = true
	}
	return out
}

// rolesOf reports which roles a principal belongs to.
//
// An undeclared principal belongs to no role. That is not the same as belonging
// to a role whose member list happens to be empty — the first is "nobody said
// who this is" and the second is "this group is empty" — but neither grants
// anything, and the distinction that matters to a policy (`actor_declared`) is
// already carried separately on the input.
func (e *Engine) rolesOf(actorID string, declared bool) map[string]bool {
	out := map[string]bool{}
	if !declared {
		return out
	}
	key := normalizeKey(actorID)
	for name, members := range e.cfg.Roles {
		for _, member := range members {
			if normalizeKey(member) == key {
				out[normalizeKey(name)] = true
				break
			}
		}
	}
	return out
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
