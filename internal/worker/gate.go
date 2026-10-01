package worker

import (
	"context"
	"time"
)

// GateAction is the runtime action requested by a pre-execution gate.
type GateAction string

const (
	GateActionAllow    GateAction = "allow"
	GateActionBlock    GateAction = "block"
	GateActionPause    GateAction = "pause"
	GateActionRetry    GateAction = "retry"
	GateActionEscalate GateAction = "escalate"
)

// GateRequest describes one task execution attempt before the handler runs.
type GateRequest struct {
	TaskID     string
	WorkflowID string
	TaskName   string
	Handler    string
	Params     map[string]interface{}
	CreatedAt  time.Time
}

// GateDecision is the runtime-facing result of a gate evaluation.
type GateDecision struct {
	ID      string
	Action  GateAction
	Reason  string
	Enforce bool
}

// GateFailurePolicy decides what the executor does when the gate itself fails to
// produce a decision, for example when a policy file cannot be read or a review
// resolver times out.
type GateFailurePolicy string

const (
	// GateFailureOpen lets the task proceed when the gate cannot decide. This is
	// the default: a malfunctioning gate must not turn into a blanket runtime
	// outage. Matches the ForgeX design rule "gate 自身报错默认 allow".
	GateFailureOpen GateFailurePolicy = "fail_open"
	// GateFailureClosed refuses the task when the gate cannot decide.
	GateFailureClosed GateFailurePolicy = "fail_closed"
)

// RuntimeGate can observe or enforce task execution before handler invocation.
// Shadow gates should return Enforce=false so the executor records the decision
// externally but does not alter runtime behavior. Enforce gates may block, pause,
// retry, or escalate by returning Enforce=true with a non-allow action.
//
// See NormalizeGateAction for what each action does at runtime today: pause,
// retry and escalate are accepted by the interface but currently collapse onto
// block, because the engine has no paused/resumed or rerouted task state.
type RuntimeGate interface {
	BeforeExecute(ctx context.Context, req GateRequest) (GateDecision, error)
}

// NormalizeGateAction maps a requested gate action onto the action the engine can
// honour. An unrecognised action is treated as block so that a malformed decision
// can never silently allow execution.
//
// Runtime effects today:
//
//	allow    -> the handler runs.
//	block    -> the handler does not run and the task is reported as failed,
//	            which is the correct effect for a refusal.
//	pause    -> the handler does not run. The engine has no paused/resumed task
//	            state, so this currently has the same runtime effect as block.
//	            Deferring without a resume path would deadlock the task; the
//	            ForgeX plan states the same reason ("pause 需要完整恢复链路，
//	            否则容易死锁").
//	retry    -> the handler does not run; the executor reports failure and the
//	            coordinator's existing retry policy decides what happens next.
//	escalate -> the handler does not run and the task is reported as failed.
//
// The requested action is always reported verbatim in the response so the audit
// trail stays truthful; only the runtime effect collapses.
func NormalizeGateAction(action GateAction) GateAction {
	switch action {
	case GateActionAllow:
		return GateActionAllow
	case GateActionBlock, GateActionPause, GateActionRetry, GateActionEscalate:
		return action
	default:
		return GateActionBlock
	}
}

// GateActionExecutesHandler reports whether an action lets the handler run.
func GateActionExecutesHandler(action GateAction) bool {
	return NormalizeGateAction(action) == GateActionAllow
}

// WithRuntimeGate returns a copy of the executor that evaluates the provided gate
// before every handler invocation. Other executor options are preserved.
func (e *Executor) WithRuntimeGate(gate RuntimeGate) *Executor {
	clone := e.clone()
	clone.gate = gate
	return clone
}

// WithGateFailurePolicy returns a copy of the executor that uses the provided
// policy when the gate itself fails. The default (zero value) is GateFailureOpen.
func (e *Executor) WithGateFailurePolicy(policy GateFailurePolicy) *Executor {
	clone := e.clone()
	clone.gateFailurePolicy = policy
	return clone
}

// clone returns a shallow copy so that options can be composed without mutating
// the receiver.
func (e *Executor) clone() *Executor {
	if e == nil {
		return &Executor{}
	}
	clone := *e
	return &clone
}
