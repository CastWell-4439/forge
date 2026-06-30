package model

import "time"

// ArtifactStatus describes the known state of a run artifact.
type ArtifactStatus string

const (
	ArtifactRequired ArtifactStatus = "required"
	ArtifactProduced ArtifactStatus = "produced"
	ArtifactMissing  ArtifactStatus = "missing"
	ArtifactValid    ArtifactStatus = "valid"
	ArtifactInvalid  ArtifactStatus = "invalid"
)

// ArtifactRecord indexes one intermediate or final artifact used by a run.
type ArtifactRecord struct {
	ID         string            `json:"id" yaml:"id"`
	RunID      string            `json:"run_id" yaml:"run_id"`
	Type       string            `json:"type" yaml:"type"`
	Status     ArtifactStatus    `json:"status" yaml:"status"`
	URI        string            `json:"uri,omitempty" yaml:"uri,omitempty"`
	Hash       string            `json:"hash,omitempty" yaml:"hash,omitempty"`
	Producer   string            `json:"producer,omitempty" yaml:"producer,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at" yaml:"created_at"`
}
