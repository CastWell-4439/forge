package model

import "time"

// ContextPack records what context was supplied to an LLM/tool step without
// embedding large artifacts directly in the prompt.
type ContextPack struct {
	ID                 string            `json:"id" yaml:"id"`
	RunID              string            `json:"run_id" yaml:"run_id"`
	NodeID             string            `json:"node_id,omitempty" yaml:"node_id,omitempty"`
	Purpose            string            `json:"purpose" yaml:"purpose"`
	Summary            string            `json:"summary" yaml:"summary"`
	ArtifactRefs       []string          `json:"artifact_refs,omitempty" yaml:"artifact_refs,omitempty"`
	IncludedBytes      int               `json:"included_bytes" yaml:"included_bytes"`
	EstimatedTokens    int               `json:"estimated_tokens" yaml:"estimated_tokens"`
	BudgetTokens       int               `json:"budget_tokens" yaml:"budget_tokens"`
	Truncated          bool              `json:"truncated" yaml:"truncated"`
	BudgetExceeded     bool              `json:"budget_exceeded" yaml:"budget_exceeded"`
	CompactionStrategy string            `json:"compaction_strategy,omitempty" yaml:"compaction_strategy,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt          time.Time         `json:"created_at" yaml:"created_at"`
}
