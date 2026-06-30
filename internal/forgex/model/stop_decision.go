package model

import "time"

// StopAction describes what the harness should do after evaluating conditions.
type StopAction string

const (
	StopActionContinue StopAction = "continue"
	StopActionRetry    StopAction = "retry"
	StopActionStop     StopAction = "stop"
	StopActionEscalate StopAction = "escalate"
	StopActionPause    StopAction = "pause"
)

// StopDecision records a stop-condition decision for one error or run state.
type StopDecision struct {
	ID         string     `json:"id" yaml:"id"`
	RunID      string     `json:"run_id" yaml:"run_id"`
	ErrorID    string     `json:"error_id,omitempty" yaml:"error_id,omitempty"`
	Action     StopAction `json:"action" yaml:"action"`
	Reason     string     `json:"reason" yaml:"reason"`
	RetryAfter string     `json:"retry_after,omitempty" yaml:"retry_after,omitempty"`
	DecidedAt  time.Time  `json:"decided_at" yaml:"decided_at"`
}
