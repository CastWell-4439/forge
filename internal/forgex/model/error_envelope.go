package model

import "time"

// ErrorEnvelope normalizes a raw tool/runtime/policy failure for classification.
type ErrorEnvelope struct {
	ID          string            `json:"id" yaml:"id"`
	RunID       string            `json:"run_id" yaml:"run_id"`
	Source      string            `json:"source" yaml:"source"`
	Operation   string            `json:"operation" yaml:"operation"`
	Message     string            `json:"message" yaml:"message"`
	RawError    string            `json:"raw_error,omitempty" yaml:"raw_error,omitempty"`
	Category    string            `json:"category,omitempty" yaml:"category,omitempty"`
	Severity    string            `json:"severity,omitempty" yaml:"severity,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty" yaml:"fingerprint,omitempty"`
	Retryable   bool              `json:"retryable" yaml:"retryable"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Timestamp   time.Time         `json:"timestamp" yaml:"timestamp"`
}
