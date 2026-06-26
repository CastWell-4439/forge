package model

import "time"

// EvalStatus is the result status of an evaluation assertion or suite.
type EvalStatus string

const (
	EvalPassed  EvalStatus = "passed"
	EvalFailed  EvalStatus = "failed"
	EvalSkipped EvalStatus = "skipped"
)

// EvalResult captures lightweight regression evaluation output.
type EvalResult struct {
	ID        string            `json:"id" yaml:"id"`
	RunID     string            `json:"run_id" yaml:"run_id"`
	SuiteID   string            `json:"suite_id" yaml:"suite_id"`
	Status    EvalStatus        `json:"status" yaml:"status"`
	Details   map[string]string `json:"details,omitempty" yaml:"details,omitempty"`
	CreatedAt time.Time         `json:"created_at" yaml:"created_at"`
}
