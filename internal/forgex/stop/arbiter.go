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
//
// The ladder, highest priority first:
//
//	human_interrupt
//	> policy_decision
//	> approval_required
//	> context_budget
//	> contract_validation
//	> retry_budget_exhausted
//	> error_envelope
//	> eval_result
//	> progress_no_change
//	> llm_suggested_done
//	> continue
//
// Hard safety signals always beat soft completion signals. The LLM's "I am done"
// sits at the bottom: it can never override any other signal, but when nothing
// else objects it does decide the run. In other words the LLM holds the lowest
// priority yet may hold the final stop authority — it has no veto over safety
// signals, and it is never consulted before them.
//
// A signal whose Suggested action is "continue" blocks nothing. A signal whose
// source is not on the ladder but which asks for a blocking action fails safe by
// escalating, so an unranked source can never be silently ignored.
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
		SignalSourceApprovalRequired,
		SignalSourceContextBudget,
		SignalSourceContractValidation,
		SignalSourceRetryBudgetExceeded,
		SignalSourceErrorEnvelope,
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

	// Fail safe on signals that are absent from the ladder. Before this guard such
	// a signal was dropped: the arbiter fell through to the LLM branch or to
	// "continue", so a blocking signal from an unranked source silently had no
	// effect at all. Escalating matches the house rule used for an unrecognised
	// failure in Engine.Decide.
	if signal, ok := firstUnrankedBlockingSignal(signals, ordered); ok {
		decision.Action = model.StopActionEscalate
		decision.Reason = fmt.Sprintf(
			"arbiter escalated unranked signal source %s: %s (signals=%s)",
			signal.Source, strings.TrimSpace(signal.Reason), strings.Join(EvidenceSummary(signals), ","))
		return decision
	}

	if signal, ok := firstSignal(signals, SignalSourceLLMSuggestedDone); ok {
		decision.Action = normalizeLLMAction(signal)
		decision.Reason = arbiterReason(signal, signals)
		return decision
	}
	return decision
}

// firstUnrankedBlockingSignal returns the first signal whose source is not on the
// ladder and which asks for anything other than continue.
func firstUnrankedBlockingSignal(signals []StopSignal, ranked []SignalSource) (StopSignal, bool) {
	for _, signal := range signals {
		if signal.Suggested == model.StopActionContinue {
			continue
		}
		if signal.Source == SignalSourceLLMSuggestedDone {
			continue // handled by the dedicated lowest-priority branch
		}
		known := false
		for _, source := range ranked {
			if signal.Source == source {
				known = true
				break
			}
		}
		if !known {
			return signal, true
		}
	}
	return StopSignal{}, false
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
	case SignalSourceApprovalRequired:
		// An action is waiting on human approval. Hold the run; a pending approval
		// must never terminate it.
		return model.StopActionPause
	case SignalSourceContextBudget, SignalSourceEvalResult, SignalSourceProgressNoChange:
		return model.StopActionPause
	case SignalSourceContractValidation:
		if suggested == model.StopActionStop {
			return model.StopActionStop
		}
		return model.StopActionPause
	case SignalSourceErrorEnvelope:
		// A classified execution error: honour an explicit stop, otherwise hold the
		// run. Retry accounting stays with the retry-budget source, which is the
		// purpose-built path for it.
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
