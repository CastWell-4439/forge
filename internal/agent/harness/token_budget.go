package harness

import (
	"log"
	"math"

	"github.com/castwell/forge/internal/agent/core"
)

// Token accounting (N3).
//
// The estimate used to be a fixed heuristic: four tokens of overhead plus one
// token per three characters. That constant is a compromise between English
// (~4 chars/token) and Chinese (~2), and it is wrong for every deployment by
// some unknown factor — which matters because the whole compaction policy is
// expressed as a fraction of the budget.
//
// The fix is NOT a tokenizer. A tokenizer would have to be chosen per model,
// and this client talks to whatever OpenAI-compatible endpoint it is pointed
// at: counting a self-hosted model's tokens with OpenAI's vocabulary is not
// precision, it is a different guess. What IS available for free is the truth:
// every response reports prompt_tokens, the exact number the provider counted.
// So the estimate is calibrated against it, online, per run.
//
// Two other things live here because they are the same question — "how much
// room is actually left":
//
//   - Output reservation: the budget covers the whole window, but MaxTokens is
//     the model's OWN reply budget. Counting the input against the full window
//     and then letting the model generate MaxTokens more is how a request that
//     "fit" comes back as a context-length error.
//   - Stability: the estimate must not depend on state that changes between
//     calls, or the same conversation would be judged differently twice.

// ObserveUsage feeds a provider's real prompt-token count back into the
// estimator (N3).
//
// estimated is what EstimateTokens said about the same message list. The ratio
// observed/estimated is what the heuristic is off by, and it is smoothed into a
// running value so a single unusual response cannot swing it. Values outside
// [minCalibrationRatio, maxCalibrationRatio] are clamped rather than discarded:
// they are still evidence, just not evidence to be taken literally.
func (cm *ContextManager) ObserveUsage(estimated, observed int) {
	if cm == nil || estimated <= 0 || observed <= 0 {
		return
	}

	ratio := float64(observed) / float64(estimated)
	if ratio < minCalibrationRatio {
		ratio = minCalibrationRatio
	}
	if ratio > maxCalibrationRatio {
		ratio = maxCalibrationRatio
	}

	cm.calibrationSamples++
	if cm.calibrationRatio == 0 {
		cm.calibrationRatio = ratio
		return
	}
	cm.calibrationRatio = cm.calibrationRatio*(1-calibrationSmoothing) + ratio*calibrationSmoothing
	// The same observation goes to the shared store, so the next run starts
	// from it instead of re-learning. Both are updated: this run keeps using
	// its own value (so its behaviour does not change mid-run because another
	// run observed something), while the store accumulates for the next one.
	cm.publishToStore(estimated, observed)
}

// Calibration reports the learned ratio and how many samples back it.
//
// Exposed so an operator (or the context.remaining tool) can tell "the estimate
// is a default guess" from "the estimate is measured against this provider" —
// a distinction that matters when a budget decision looks surprising.
func (cm *ContextManager) Calibration() (ratio float64, samples int) {
	if cm == nil {
		return 0, 0
	}
	return cm.calibrationRatio, cm.calibrationSamples
}

// calibrated converts a raw heuristic count into the calibrated estimate.
//
// Before enough samples have accumulated the raw value stands: two observations
// are a coincidence, not a ratio. The count is rounded UP so a partial token
// never makes the estimate optimistic — under-counting is the failure mode that
// ends in a provider error.
func (cm *ContextManager) calibrated(raw int) int {
	if cm == nil || cm.calibrationSamples < calibrationSamples || cm.calibrationRatio <= 0 {
		return raw
	}
	adjusted := math.Ceil(float64(raw) * cm.calibrationRatio)
	if adjusted > float64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int(adjusted)
}

// estimateTokens is the form of EstimateTokens the manager's decisions use:
// anchored to the provider's last real count when one is available, then
// calibrated.
//
// Callers inside the manager use it; EstimateTokens itself stays a pure
// function (no hidden state) so it remains testable and predictable — the
// anchor lives here, in the manager, and is visible through HasAnchor.
func (cm *ContextManager) estimateTokens(messages []core.Message) int {
	if cm == nil {
		return EstimateTokens(messages)
	}
	base, delta := cm.anchoredEstimate(messages)
	return cm.calibrated(base + delta)
}

// SetOutputReserve tells the manager how many tokens to hold back for the
// model's reply (N3). Zero means no reservation.
func (cm *ContextManager) SetOutputReserve(tokens int) {
	if cm == nil {
		return
	}
	if tokens < 0 {
		tokens = 0
	}
	cm.reserveOutput = tokens
}

// inputBudget is the room actually available for INPUT.
//
// It is the window minus the reply reservation, and it is floored so a
// misconfiguration cannot drive it to zero or negative — a budget of nothing
// would compact the conversation away on every step, which is a worse failure
// than the overflow the reservation was meant to prevent.
//
// The floor is a fraction of the window rather than a constant: a deployment
// with a small window still needs a usable share of it.
//
// The reservation is NOT cleared when it is unaffordable. Clearing it would
// make this function return different answers on successive calls (floor the
// first time, full window the next), which is exactly the instability that
// makes a budget unpredictable. The warning is emitted once via the flag.
func (cm *ContextManager) inputBudget() int {
	if cm == nil || cm.maxTokens <= 0 {
		return DefaultMaxContextTokens
	}
	floor := cm.maxTokens / 4
	if cm.reserveOutput <= 0 {
		return cm.maxTokens
	}
	budget := cm.maxTokens - cm.reserveOutput
	if budget < floor {
		// Warn once rather than silently ignoring the setting: the operator
		// asked for a reservation this window cannot afford.
		if !cm.reserveWarned {
			log.Printf("WARN: output reservation %d tokens exceeds the safe share of a %d-token window; "+
				"using %d tokens as the input budget", cm.reserveOutput, cm.maxTokens, floor)
			cm.reserveWarned = true
		}
		return floor
	}
	return budget
}
