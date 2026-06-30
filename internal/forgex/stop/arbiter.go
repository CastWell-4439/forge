package stop

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// Arbiter decides what a run should do when multiple hard and soft stop signals
// are present. Hard safety signals always beat soft completion signals.
type Arbiter struct {
	seq uint64
	now func() time.Time
}

var arbiterSeq atomic.Uint64

// NewArbiter creates a termination arbiter.
func NewArbiter() *Arbiter {
	return &Arbiter{now: time.Now}
}

// Decide arbitrates signals using the M5 priority order.
func (a *Arbiter) Decide(runID string, signals []StopSignal) model.StopDecision {
	if a == nil {
		a = NewArbiter()
	}
	decision := model.StopDecision{
		ID:        a.newID(runID),
		RunID:     runID,
		Action:    model.StopActionContinue,
		Reason:    "no blocking termination signals; continue",
		DecidedAt: a.now().UTC(),
	}
	if len(signals) == 0 {
		return decision
	}

	ordered := []SignalSource{
		SignalSourceHumanInterrupt,
		SignalSourcePolicyDecision,
		SignalSourceContextBudget,
		SignalSourceContractValidation,
		SignalSourceRetryBudgetExceeded,
		SignalSourceEvalResult,
		SignalSourceProgressNoChange,
	}
	for _, source := range ordered {
		if signal, ok := firstBlockingSignal(signals, source); ok {
			decision.Action = normalizeAction(source, signal)
			decision.Reason = arbiterReason(signal, signals)
			return decision
		}
	}

	if signal, ok := firstSignal(signals, SignalSourceLLMSuggestedDone); ok {
		decision.Action = normalizeLLMAction(signal)
		decision.Reason = arbiterReason(signal, signals)
		return decision
	}
	return decision
}

func firstBlockingSignal(signals []StopSignal, source SignalSource) (StopSignal, bool) {
	for _, signal := range signals {
		if signal.Source != source {
			continue
		}
		if signal.Suggested == model.StopActionContinue {
			continue
		}
		return signal, true
	}
	return StopSignal{}, false
}

func firstSignal(signals []StopSignal, source SignalSource) (StopSignal, bool) {
	for _, signal := range signals {
		if signal.Source == source {
			return signal, true
		}
	}
	return StopSignal{}, false
}

func normalizeAction(source SignalSource, signal StopSignal) model.StopAction {
	suggested := signal.Suggested
	if suggested == "" {
		suggested = model.StopActionPause
	}
	switch source {
	case SignalSourceHumanInterrupt:
		if suggested == model.StopActionStop {
			return model.StopActionStop
		}
		return model.StopActionPause
	case SignalSourcePolicyDecision:
		if suggested == model.StopActionStop || strings.Contains(strings.ToLower(signal.Reason), "deny") {
			return model.StopActionStop
		}
		if suggested == model.StopActionEscalate {
			return model.StopActionEscalate
		}
		return model.StopActionPause
	case SignalSourceContextBudget, SignalSourceEvalResult, SignalSourceProgressNoChange:
		return model.StopActionPause
	case SignalSourceContractValidation:
		if suggested == model.StopActionStop {
			return model.StopActionStop
		}
		return model.StopActionPause
	case SignalSourceRetryBudgetExceeded:
		return model.StopActionEscalate
	default:
		return suggested
	}
}

func normalizeLLMAction(signal StopSignal) model.StopAction {
	if signal.Suggested == model.StopActionStop {
		return model.StopActionStop
	}
	return model.StopActionContinue
}

func arbiterReason(winner StopSignal, signals []StopSignal) string {
	reason := strings.TrimSpace(winner.Reason)
	if reason == "" {
		reason = fmt.Sprintf("matched termination signal from %s", winner.Source)
	}
	return fmt.Sprintf("arbiter selected %s signal %s: %s (signals=%s)", winner.Source, winner.ID, reason, strings.Join(EvidenceSummary(signals), ","))
}

func (a *Arbiter) newID(runID string) string {
	seq := atomic.AddUint64(&a.seq, 1)
	if seq == 0 {
		seq = arbiterSeq.Add(1)
	}
	if strings.TrimSpace(runID) == "" {
		runID = "run"
	}
	return fmt.Sprintf("arbiter-%s-%d", runID, seq)
}
