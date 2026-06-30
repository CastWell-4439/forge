package model

import "time"

// ContractValidation records one tool contract validator outcome.
type ContractValidation struct {
	ID        string    `json:"id" yaml:"id"`
	RunID     string    `json:"run_id" yaml:"run_id"`
	ToolName  string    `json:"tool_name" yaml:"tool_name"`
	Status    string    `json:"status" yaml:"status"`
	Validator string    `json:"validator" yaml:"validator"`
	Message   string    `json:"message" yaml:"message"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}
