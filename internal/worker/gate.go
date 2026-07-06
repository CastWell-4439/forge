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

// RuntimeGate can observe or enforce task execution before handler invocation.
// Shadow gates should return Enforce=false so the executor records the decision
// externally but does not alter runtime behavior. Enforce gates may block, pause,
// retry, or escalate by returning Enforce=true with a non-allow action.
type RuntimeGate interface {
	BeforeExecute(ctx context.Context, req GateRequest) (GateDecision, error)
}

// WithRuntimeGate returns a shallow executor copy that evaluates the provided
// gate before every handler invocation.
func (e *Executor) WithRuntimeGate(gate RuntimeGate) *Executor {
	if e == nil {
		return &Executor{gate: gate}
	}
	return &Executor{registry: e.registry, gate: gate}
}
