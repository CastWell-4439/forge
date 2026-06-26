package model

import "time"

// Span represents a trace span in a ForgeX run.
type Span struct {
	ID        string         `json:"id" yaml:"id"`
	RunID     string         `json:"run_id" yaml:"run_id"`
	ParentID  string         `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Name      string         `json:"name" yaml:"name"`
	StartedAt time.Time      `json:"started_at" yaml:"started_at"`
	EndedAt   time.Time      `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
	Status    string         `json:"status" yaml:"status"`
	Attrs     map[string]any `json:"attrs,omitempty" yaml:"attrs,omitempty"`
}
