package model

import "time"

// Artifact describes a file or external object produced during a run.
type Artifact struct {
	ID        string            `json:"id" yaml:"id"`
	RunID     string            `json:"run_id" yaml:"run_id"`
	Name      string            `json:"name" yaml:"name"`
	Kind      string            `json:"kind" yaml:"kind"`
	Path      string            `json:"path,omitempty" yaml:"path,omitempty"`
	URI       string            `json:"uri,omitempty" yaml:"uri,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at" yaml:"created_at"`
}
