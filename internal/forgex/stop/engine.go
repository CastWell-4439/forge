package stop

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/castwell/forge/internal/forgex/failure"
	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// LoadPolicy reads and parses a stop-policy YAML file.
func LoadPolicy(path string) (*PolicyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stop policy %s: %w", path, err)
	}

	var cfg PolicyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse stop policy %s: %w", path, err)
	}
	return &cfg, nil
}

// Engine evaluates ErrorEnvelopes against a PolicyConfig and produces
// StopDecisions, tracking per-fingerprint retry usage across calls.
type Engine struct {
	policy *PolicyConfig

	mu      sync.Mutex
	retries *RetryState
	seq     uint64
	now     func() time.Time
}

// NewEngine builds an Engine from a policy. A nil policy is treated as an empty
// configuration (no policies, zero default budget).
func NewEngine(policy *PolicyConfig) *Engine {
	if policy == nil {
		policy = &PolicyConfig{}
	}
	return &Engine{
		policy:  policy,
		retries: NewRetryState(),
		now:     time.Now,
	}
}

// Decide evaluates an envelope for a run and returns a StopDecision.
//
// Order of evaluation:
//   - (a) the first policy whose When matches wins;
//   - (b) a matched retry policy retries within budget, else escalates, and the
//     fingerprint's retry count is incremented on each retry;
//   - non-retry policies map directly to their action (stop/escalate/continue);
//   - (c) when no policy matches, non-retryable contract/policy errors stop;
//   - (e) retryable errors fall back to the retry-budget logic;
//   - (d) unknown failures escalate for human inspection.
func (e *Engine) Decide(runID string, envelope model.ErrorEnvelope) model.StopDecision {
	e.mu.Lock()
	defer e.mu.Unlock()

	category := strings.ToLower(strings.TrimSpace(envelope.Category))
	severity := strings.ToLower(strings.TrimSpace(envelope.Severity))
	fingerprint := e.fingerprint(envelope)

	decision := model.StopDecision{
		ID:        e.newID(runID),
		RunID:     runID,
		ErrorID:   envelope.ID,
		DecidedAt: e.now().UTC(),
	}

	// (a) first matching policy wins.
	if policy, ok := e.matchPolicy(category, severity); ok {
		if policy.StopAction() == model.StopActionRetry {
			// (b) retry policy: honour the budget.
			e.decideRetry(&decision, envelope, category, fingerprint, policy)
			return decision
		}
		decision.Action = policy.StopAction()
		decision.Reason = policyReason(policy)
		return decision
	}

	// No policy matched: fall back to envelope semantics.
	e.fallback(&decision, envelope, category, severity, fingerprint)
	return decision
}

// DecideContextBudget evaluates context budget pressure as a run-level stop policy.
func (e *Engine) DecideContextBudget(runID string, totalTokens, budgetTokens int) model.StopDecision {
	e.mu.Lock()
	defer e.mu.Unlock()

	decision := model.StopDecision{
		ID:        e.newID(runID),
		RunID:     runID,
		DecidedAt: e.now().UTC(),
	}
	if budgetTokens <= 0 || totalTokens <= budgetTokens {
		decision.Action = model.StopActionContinue
		decision.Reason = fmt.Sprintf("context budget within limit (%d/%d)", totalTokens, budgetTokens)
		return decision
	}
	if policy, ok := e.matchPolicy("context_budget_exceeded", "high"); ok {
		decision.Action = policy.StopAction()
		decision.Reason = policyReason(policy)
		return decision
	}
	decision.Action = model.StopActionPause
	decision.Reason = fmt.Sprintf("context budget exceeded (%d/%d); snapshot and pause", totalTokens, budgetTokens)
	return decision
}

// DecideProgressNoChange evaluates stagnant progress as a run-level stop policy.
func (e *Engine) DecideProgressNoChange(runID string, noChangeTurns, threshold int) model.StopDecision {
	e.mu.Lock()
	defer e.mu.Unlock()

	decision := model.StopDecision{
		ID:        e.newID(runID),
		RunID:     runID,
		DecidedAt: e.now().UTC(),
	}
	if threshold <= 0 {
		threshold = 5
	}
	if noChangeTurns < threshold {
		decision.Action = model.StopActionContinue
		decision.Reason = fmt.Sprintf("progress still changing (%d/%d no-change turns)", noChangeTurns, threshold)
		return decision
	}
	if policy, ok := e.matchPolicy("progress_no_change", "medium"); ok {
		decision.Action = policy.StopAction()
		decision.Reason = policyReason(policy)
		return decision
	}
	decision.Action = model.StopActionPause
	decision.Reason = fmt.Sprintf("progress did not change for %d turns; pause for human review", noChangeTurns)
	return decision
}

// decideRetry applies the retry budget for a matched retry policy, mutating the
// decision into a retry (within budget) or an escalate (budget exhausted).
func (e *Engine) decideRetry(
	decision *model.StopDecision,
	envelope model.ErrorEnvelope,
	category, fingerprint string,
	policy StopPolicy,
) {
	if !envelope.Retryable {
		decision.Action = model.StopActionEscalate
		decision.Reason = fmt.Sprintf("policy %s suggests retry but error is not retryable; escalating", policy.ID)
		return
	}
	e.applyBudget(decision, category, fingerprint, policyReason(policy))
}

// fallback decides when no policy matched, using the envelope's own
// classification.
func (e *Engine) fallback(
	decision *model.StopDecision,
	envelope model.ErrorEnvelope,
	category, severity, fingerprint string,
) {
	// (c) non-retryable contract/policy violations cannot succeed on retry.
	if !envelope.Retryable && isContractOrPolicy(category) {
		decision.Action = model.StopActionStop
		decision.Reason = fmt.Sprintf("non-retryable %s error; stopping", category)
		return
	}

	// (e) retryable errors fall back to the budget logic.
	if envelope.Retryable {
		e.applyBudget(decision, category, fingerprint, "retryable error, no matching policy")
		return
	}

	// (d) unknown / unclassified failures escalate for human inspection
	// regardless of severity, which keeps the outcome stable.
	if category == "" || category == "unknown" {
		decision.Action = model.StopActionEscalate
		decision.Reason = fmt.Sprintf("unknown failure (severity %s) with no matching policy; escalating for inspection", severityOrUnset(severity))
		return
	}

	// Default safe outcome: a classified but non-retryable error halts.
	decision.Action = model.StopActionStop
	decision.Reason = fmt.Sprintf("non-retryable %s error with no matching policy; stopping", category)
}

// applyBudget retries while the fingerprint is under budget and escalates once
// the budget is reached, incrementing the retry count on each retry.
func (e *Engine) applyBudget(decision *model.StopDecision, category, fingerprint, reason string) {
	budget := e.policy.RetryBudget.budgetFor(category)
	used := e.retries.Count(fingerprint)

	if used >= budget {
		decision.Action = model.StopActionEscalate
		decision.Reason = fmt.Sprintf("retry budget exhausted for %s (%d/%d): %s", category, used, budget, reason)
		return
	}

	e.retries.Inc(fingerprint)
	decision.Action = model.StopActionRetry
	decision.Reason = fmt.Sprintf("%s (retry %d/%d)", reason, used+1, budget)
}

// matchPolicy returns the first policy whose When matches the category/severity.
func (e *Engine) matchPolicy(category, severity string) (StopPolicy, bool) {
	for _, p := range e.policy.Policies {
		if p.When.matches(category, severity) {
			return p, true
		}
	}
	return StopPolicy{}, false
}

// fingerprint returns the envelope's fingerprint, computing a stable one when the
// envelope was not pre-classified so retry budgets stay isolated per failure.
func (e *Engine) fingerprint(envelope model.ErrorEnvelope) string {
	if envelope.Fingerprint != "" {
		return envelope.Fingerprint
	}
	return failure.Fingerprint(envelope)
}

// newID builds a stable, unique decision id. The exact value is not part of the
// contract; tests only assert it is non-empty.
func (e *Engine) newID(runID string) string {
	e.seq++
	run := runID
	if run == "" {
		run = "run"
	}
	return "stopdec-" + run + "-" + strconv.FormatUint(e.seq, 10)
}

// policyReason returns a human-readable reason for a policy, falling back to the
// policy id when no reason text was configured.
func policyReason(p StopPolicy) string {
	if strings.TrimSpace(p.Reason) != "" {
		return p.Reason
	}
	return fmt.Sprintf("matched policy %s", p.ID)
}

// isContractOrPolicy reports whether a category denotes a contract or policy
// violation, which is not retryable without changing inputs.
func isContractOrPolicy(category string) bool {
	return strings.Contains(category, "contract") || strings.Contains(category, "policy")
}

// severityOrUnset renders an empty severity as a readable placeholder.
func severityOrUnset(severity string) string {
	if severity == "" {
		return "unset"
	}
	return severity
}
