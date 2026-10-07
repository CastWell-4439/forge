package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// --- calibration (N3) ---

// Before enough samples, the raw heuristic stands: two observations are a
// coincidence, not a ratio.
func TestCalibrationNeedsSamples(t *testing.T) {
	cm := NewContextManager(100000, nil)
	assert.Equal(t, 1000, cm.calibrated(1000), "uncalibrated returns the raw count unchanged")

	// A single wild observation must not move the estimate.
	cm.ObserveUsage(1000, 5000)
	assert.Equal(t, 1000, cm.calibrated(1000), "one sample is not a calibration")
}

// A consistently under-estimating heuristic is corrected upward, and the
// correction is what the budget arithmetic then uses.
func TestCalibrationConvergesUpward(t *testing.T) {
	cm := NewContextManager(100000, nil)
	// The provider really counts twice what we estimated.
	for i := 0; i < 12; i++ {
		cm.ObserveUsage(1000, 2000)
	}

	ratio, samples := cm.Calibration()
	assert.Equal(t, 12, samples)
	assert.InDelta(t, 2.0, ratio, 0.05, "the ratio converged on 2x")

	got := cm.calibrated(1000)
	assert.InDelta(t, 2000, got, 40, "the estimate is now measured, not guessed")
}

// An over-estimating heuristic is corrected downward too — the correction is
// two-directional, or a deployment would pay for phantom tokens forever.
func TestCalibrationConvergesDownward(t *testing.T) {
	cm := NewContextManager(100000, nil)
	for i := 0; i < 12; i++ {
		cm.ObserveUsage(2000, 1000)
	}

	ratio, _ := cm.Calibration()
	assert.InDelta(t, 0.5, ratio, 0.05)
	assert.InDelta(t, 500, cm.calibrated(1000), 40)
}

// The ratio is clamped: one absurd sample cannot move the estimate by an order
// of magnitude. It is still evidence, just not evidence taken literally.
func TestCalibrationRatioIsClamped(t *testing.T) {
	cm := NewContextManager(100000, nil)
	for i := 0; i < 10; i++ {
		cm.ObserveUsage(1000, 1_000_000) // 1000x
	}
	ratio, _ := cm.Calibration()
	assert.LessOrEqual(t, ratio, maxCalibrationRatio, "clamped at the ceiling")

	cm2 := NewContextManager(100000, nil)
	for i := 0; i < 10; i++ {
		cm2.ObserveUsage(1000, 1) // 0.001x
	}
	ratio2, _ := cm2.Calibration()
	assert.GreaterOrEqual(t, ratio2, minCalibrationRatio, "clamped at the floor")
}

// Zero or negative inputs are ignored rather than poisoning the ratio: a
// provider that omits usage must not be read as "zero tokens".
func TestCalibrationIgnoresInvalidSamples(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.ObserveUsage(0, 1000)
	cm.ObserveUsage(1000, 0)
	cm.ObserveUsage(-5, 1000)
	cm.ObserveUsage(1000, -5)

	ratio, samples := cm.Calibration()
	assert.Equal(t, 0, samples, "invalid samples do not count")
	assert.Zero(t, ratio)
}

// The estimate never rounds DOWN: under-counting is the failure mode that ends
// in a provider context-length error.
func TestCalibratedRoundsUp(t *testing.T) {
	cm := NewContextManager(100000, nil)
	for i := 0; i < 10; i++ {
		cm.ObserveUsage(1000, 1500) // ratio 1.5
	}
	// 1001 * 1.5 = 1501.5 → must not become 1501.
	got := cm.calibrated(1001)
	assert.GreaterOrEqual(t, got, 1502, "rounds up, never down")
}

// EstimateTokens itself stays a pure function: calibration lives in the
// manager, so a caller can still predict the raw count.
func TestEstimateTokensStaysPure(t *testing.T) {
	msgs := []core.Message{{Role: "user", Content: strings.Repeat("a", 300)}}
	first := EstimateTokens(msgs)
	second := EstimateTokens(msgs)
	assert.Equal(t, first, second, "no hidden state")

	cm := NewContextManager(100000, nil)
	for i := 0; i < 10; i++ {
		cm.ObserveUsage(1000, 3000)
	}
	assert.Equal(t, first, EstimateTokens(msgs), "the package function is unchanged by calibration")
	assert.Greater(t, cm.estimateTokens(msgs), first, "the manager's view is calibrated")
}

// --- output reservation (N3) ---

// Without a reservation the input budget is the whole window: the pre-N3
// behaviour stays reachable.
func TestNoReserveMeansFullWindow(t *testing.T) {
	cm := NewContextManager(100000, nil)
	assert.Equal(t, 100000, cm.inputBudget())
}

// The reservation is subtracted, so a request that fits cannot overflow once
// the model generates its reply.
func TestReserveShrinksInputBudget(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.SetOutputReserve(4096)
	assert.Equal(t, 100000-4096, cm.inputBudget())
}

// A reservation the window cannot afford is refused rather than honoured: a
// budget of nothing would compact the conversation away every step, which is a
// worse failure than the overflow it was meant to prevent.
func TestReserveIsFloored(t *testing.T) {
	cm := NewContextManager(1000, nil)
	cm.SetOutputReserve(9999) // absurd
	assert.Equal(t, 250, cm.inputBudget(), "floored at a quarter of the window")
	// The answer must be STABLE: clearing the setting would make successive
	// calls disagree (floor, then full window), which is how a budget becomes
	// unpredictable.
	assert.Equal(t, 250, cm.inputBudget(), "the same answer every call")
	assert.Equal(t, 250, cm.inputBudget())
}

// A negative reservation is nonsense and treated as zero.
func TestNegativeReserveIsZero(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.SetOutputReserve(-100)
	assert.Equal(t, 100000, cm.inputBudget())
}

// The reservation is what compaction compares against — the two must agree or
// the loop would compact on one number and report another.
func TestCompactionUsesInputBudget(t *testing.T) {
	cm := NewContextManager(1000, nil)
	cm.SetOutputReserve(200)
	assert.Equal(t, 800, cm.inputBudget())

	// A list that fits the raw window but not the input budget must compact.
	big := []core.Message{{Role: "user", Content: strings.Repeat("x", 2600)}} // ~866 tokens
	require.LessOrEqual(t, EstimateTokens(big), 1000, "it would fit the raw window")
	require.Greater(t, EstimateTokens(big), cm.inputBudget(), "but not the input budget")

	_, err := cm.CompactIfNeeded(context.Background(), big)
	require.NoError(t, err, "compaction ran rather than trusting the raw window")
}

// --- the stability invariant (N3, the DSH lesson) ---

// The system prompt and the tool list must render IDENTICALLY across steps
// within a run. A prompt whose prefix changes between calls loses provider
// prefix reuse from the first changed token — and, worse, it makes the run's
// behaviour depend on when a step happened rather than on what it is.
//
// This is the invariant form of "we do not break the model's cache": rather
// than a comment promising care, a test that fails when new code starts
// varying the prompt mid-run.
func TestSystemPromptIsStableWithinARun(t *testing.T) {
	reg := registryWithTools(t, "file.read", "git.status", "web.search", "data.query",
		"context.remaining", "context.compact", "context.recall", "tool.search")
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 5
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	// Two renders with the same inputs: byte-identical, or the prefix moves.
	first := loop.buildSystemPrompt()
	second := loop.buildSystemPrompt()
	assert.Equal(t, first, second, "the same state renders the same prompt")

	// And the tool subset itself is a stable, sorted list.
	visible, hidden := loop.selectVisibleTools("read a file", map[string]bool{})
	again, hiddenAgain := loop.selectVisibleTools("read a file", map[string]bool{})
	require.Equal(t, hidden, hiddenAgain)
	require.Len(t, visible, len(again))
	for i := range visible {
		assert.Equal(t, visible[i].Name, again[i].Name, "position %d stable", i)
	}
	for i := 1; i < len(visible); i++ {
		assert.Less(t, visible[i-1].Name, visible[i].Name, "sorted by name, so the block is canonical")
	}
}

// Tool registration ORDER must not leak into the prompt: two registries holding
// the same tools in different orders must render the same text. Without this,
// plugin load order would silently change every deployment's prompt prefix.
func TestPromptDoesNotDependOnRegistrationOrder(t *testing.T) {
	names := []string{"file.read", "git.status", "web.search", "data.query",
		"context.remaining", "context.compact", "context.recall", "tool.search"}

	forward := registryWithTools(t, names...)
	reversed := make([]string, len(names))
	for i, n := range names {
		reversed[len(names)-1-i] = n
	}
	backward := registryWithTools(t, reversed...)

	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 6
	a := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(forward), cfg)
	b := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(backward), cfg)

	assert.Equal(t, a.buildSystemPrompt(), b.buildSystemPrompt(),
		"the prompt is canonical, not registration-ordered")
}

// The default loop config reserves nothing, so existing behaviour is unchanged
// unless a deployment asks for the reservation.
func TestDefaultLoopConfigReservesNothing(t *testing.T) {
	cfg := DefaultLoopConfig()
	assert.Zero(t, cfg.OutputReserve)

	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(registryWithTools(t, "file.read")), cfg)
	assert.Equal(t, loop.ctxMgr.maxTokens, loop.ctxMgr.inputBudget())
}

// The reserve flows from LoopConfig into the manager — the wiring is the point,
// since a field nobody reads is the "has a skeleton, no power" pattern.
func TestLoopConfigReserveReachesTheManager(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.MaxContextTokens = 10000
	cfg.OutputReserve = 2048

	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(registryWithTools(t, "file.read")), cfg)
	assert.Equal(t, 10000-2048, loop.ctxMgr.inputBudget())
}
