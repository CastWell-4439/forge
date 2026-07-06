package model

import "time"

// GateMode describes whether a runtime gate only observes or may enforce.
type GateMode string

const (
	GateModeShadow  GateMode = "shadow"
	GateModeEnforce GateMode = "enforce"
)

// GateAction is the normalized product action suggested by a runtime gate.
type GateAction string

const (
	GateActionAllow    GateAction = "allow"
	GateActionPause    GateAction = "pause"
	GateActionBlock    GateAction = "block"
	GateActionRetry    GateAction = "retry"
	GateActionEscalate GateAction = "escalate"
)

// GateDecision records one explainable RuntimeGate decision.
//
// Shadow decisions never alter Forge runtime execution semantics. Enforce
// decisions may be applied by a runtime gate integration before handler/tool
// execution and should remain fully explainable through this artifact.
type GateDecision struct {
	ID         string     `json:"id" yaml:"id"`
	RunID      string     `json:"run_id" yaml:"run_id"`
	Mode       GateMode   `json:"mode" yaml:"mode"`
	Action     GateAction `json:"action" yaml:"action"`
	Scope      string     `json:"scope,omitempty" yaml:"scope,omitempty"`
	SubjectID  string     `json:"subject_id,omitempty" yaml:"subject_id,omitempty"`
	Reason     string     `json:"reason" yaml:"reason"`
	Evidence   []string   `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Source     string     `json:"source,omitempty" yaml:"source,omitempty"`
	NeedsHuman bool       `json:"needs_human,omitempty" yaml:"needs_human,omitempty"`
	CreatedAt  time.Time  `json:"created_at" yaml:"created_at"`
}
