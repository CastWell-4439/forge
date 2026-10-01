package model

import "time"

// StateValidation records one claim validator outcome.
//
// It is the claim-scoped counterpart of ContractValidation. That type is keyed by
// ToolName and describes a tool contract check, so it cannot represent a check on
// a world-state claim, which is keyed by the state key and identified by claim id.
// The field set is otherwise deliberately identical.
type StateValidation struct {
	ID        string    `json:"id" yaml:"id"`
	RunID     string    `json:"run_id" yaml:"run_id"`
	ClaimID   string    `json:"claim_id" yaml:"claim_id"`
	Key       string    `json:"key" yaml:"key"`
	Status    string    `json:"status" yaml:"status"`
	Validator string    `json:"validator" yaml:"validator"`
	Message   string    `json:"message" yaml:"message"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}
