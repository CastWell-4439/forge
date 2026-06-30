package toolgw

import "time"

// ValidationStatus is the result of a contract validation check.
type ValidationStatus string

const (
	ValidationPassed ValidationStatus = "passed"
	ValidationFailed ValidationStatus = "failed"
)

// ValidationResult records one validator outcome for a tool call.
type ValidationResult struct {
	ID        string           `json:"id" yaml:"id"`
	RunID     string           `json:"run_id" yaml:"run_id"`
	ToolName  string           `json:"tool_name" yaml:"tool_name"`
	Status    ValidationStatus `json:"status" yaml:"status"`
	Validator string           `json:"validator" yaml:"validator"`
	Message   string           `json:"message" yaml:"message"`
	CreatedAt time.Time        `json:"created_at" yaml:"created_at"`
}
