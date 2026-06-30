package model

import "time"

// EvalStatus is the result status of an evaluation assertion or suite.
type EvalStatus string

const (
	EvalPassed  EvalStatus = "passed"
	EvalFailed  EvalStatus = "failed"
	EvalSkipped EvalStatus = "skipped"
)

// EvalAssertionResult captures one path/op/value assertion outcome.
type EvalAssertionResult struct {
	Path     string     `json:"path" yaml:"path"`
	Op       string     `json:"op" yaml:"op"`
	Expected string     `json:"expected" yaml:"expected"`
	Actual   string     `json:"actual,omitempty" yaml:"actual,omitempty"`
	Status   EvalStatus `json:"status" yaml:"status"`
	Message  string     `json:"message,omitempty" yaml:"message,omitempty"`
}

// EvalCaseResult captures all assertion outcomes for one case.
type EvalCaseResult struct {
	ID         string                `json:"id" yaml:"id"`
	Status     EvalStatus            `json:"status" yaml:"status"`
	Assertions []EvalAssertionResult `json:"assertions" yaml:"assertions"`
}

// EvalResult captures lightweight regression evaluation output.
type EvalResult struct {
	ID        string            `json:"id" yaml:"id"`
	RunID     string            `json:"run_id" yaml:"run_id"`
	SuiteID   string            `json:"suite_id" yaml:"suite_id"`
	Status    EvalStatus        `json:"status" yaml:"status"`
	Cases     []EvalCaseResult  `json:"cases,omitempty" yaml:"cases,omitempty"`
	Details   map[string]string `json:"details,omitempty" yaml:"details,omitempty"`
	CreatedAt time.Time         `json:"created_at" yaml:"created_at"`
}
