package model

// TaskPacket is the normalized input contract for a ForgeX harness run.
type TaskPacket struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	Goal        string            `json:"goal" yaml:"goal"`
	Authority   string            `json:"authority_level,omitempty" yaml:"authority_level,omitempty"`
	Inputs      map[string]any    `json:"inputs" yaml:"inputs"`
	Constraints []string          `json:"constraints,omitempty" yaml:"constraints,omitempty"`
	Success     []string          `json:"success,omitempty" yaml:"success,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}
