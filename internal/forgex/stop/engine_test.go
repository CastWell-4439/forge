package stop

import (
	"path/filepath"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// policyPath points at the repository's real stop-policy config relative to this
// package directory (internal/forgex/stop -> repo root).
var policyPath = filepath.Join("..", "..", "..", "configs", "forgex", "stop_policies.yaml")

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	cfg, err := LoadPolicy(policyPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotEmpty(t, cfg.Policies)
	return NewEngine(cfg)
}

func TestLoadPolicyMissingFile(t *testing.T) {
	_, err := LoadPolicy(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestLoadPolicyContents(t *testing.T) {
	cfg, err := LoadPolicy(policyPath)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, 2, cfg.RetryBudget.DefaultMaxRetries)
	assert.Equal(t, 3, cfg.RetryBudget.ByCategory["transient_timeout"])
	assert.Equal(t, 0, cfg.RetryBudget.ByCategory["tool_contract_violation"])
}

func TestDecideContractViolationStops(t *testing.T) {
	e := newTestEngine(t)

	env := model.ErrorEnvelope{
		ID:        "err-contract",
		RunID:     "run-1",
		Category:  "tool_contract_violation",
		Severity:  "high",
		Retryable: false,
	}

	got := e.Decide("run-1", env)

	assert.Equal(t, model.StopActionStop, got.Action)
	// Decision must be fully populated.
	assert.NotEmpty(t, got.ID)
	assert.Equal(t, "run-1", got.RunID)
	assert.Equal(t, "err-contract", got.ErrorID)
	assert.NotEmpty(t, got.Reason)
	assert.False(t, got.DecidedAt.IsZero())
}

func TestDecideTimeoutFirstRetries(t *testing.T) {
	e := newTestEngine(t)

	got := e.Decide("run-2", timeoutEnvelope())

	assert.Equal(t, model.StopActionRetry, got.Action)
	assert.Contains(t, got.Reason, "retry 1/3")
}

func TestDecideTimeoutEscalatesPastBudget(t *testing.T) {
	e := newTestEngine(t)

	// Budget for transient_timeout is 3: three retries then escalate.
	for i := 1; i <= 3; i++ {
		got := e.Decide("run-3", timeoutEnvelope())
		require.Equal(t, model.StopActionRetry, got.Action, "call %d should retry", i)
	}

	got := e.Decide("run-3", timeoutEnvelope())
	assert.Equal(t, model.StopActionEscalate, got.Action)
	assert.Contains(t, got.Reason, "budget exhausted")
}

func TestDecideUnknownHighEscalates(t *testing.T) {
	e := newTestEngine(t)

	env := model.ErrorEnvelope{
		ID:        "err-unknown",
		Category:  "unknown",
		Severity:  "high",
		Retryable: false,
	}

	got := e.Decide("run-4", env)
	assert.Equal(t, model.StopActionEscalate, got.Action)
	assert.NotEmpty(t, got.Reason)
}

// unknown/medium has no matching policy and falls back to escalate for
// inspection, which must be stable across calls.
func TestDecideUnknownMediumEscalates(t *testing.T) {
	e := newTestEngine(t)

	env := model.ErrorEnvelope{
		Category:  "unknown",
		Severity:  "medium",
		Retryable: false,
	}

	first := e.Decide("run-5", env)
	second := e.Decide("run-5", env)
	assert.Equal(t, model.StopActionEscalate, first.Action)
	assert.Equal(t, first.Action, second.Action, "decision must be stable")
}

// Retry budgets are tracked per fingerprint: exhausting one does not affect
// another distinct failure.
func TestRetryBudgetIsolatedByFingerprint(t *testing.T) {
	e := newTestEngine(t)

	envA := timeoutEnvelope()
	envB := timeoutEnvelope()
	envB.Operation = "tool.other"
	envB.Message = "request timeout on a different operation"

	require.NotEqual(t, e.fingerprint(envA), e.fingerprint(envB))

	// Exhaust envA's budget (3 retries + 1 escalate).
	for i := 0; i < 3; i++ {
		require.Equal(t, model.StopActionRetry, e.Decide("run-6", envA).Action)
	}
	assert.Equal(t, model.StopActionEscalate, e.Decide("run-6", envA).Action)

	// envB still has its full budget.
	assert.Equal(t, model.StopActionRetry, e.Decide("run-6", envB).Action)
}

func TestDecisionIDsAreUnique(t *testing.T) {
	e := newTestEngine(t)

	first := e.Decide("run-7", timeoutEnvelope())
	second := e.Decide("run-7", timeoutEnvelope())
	assert.NotEqual(t, first.ID, second.ID)
}

// timeoutEnvelope is a classified transient timeout failure used by retry tests.
func timeoutEnvelope() model.ErrorEnvelope {
	return model.ErrorEnvelope{
		ID:        "err-timeout",
		RunID:     "run",
		Source:    "tool",
		Operation: "tool.fetch",
		Message:   "request timeout after 30s",
		Category:  "transient_timeout",
		Severity:  "medium",
		Retryable: true,
	}
}
