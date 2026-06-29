// Package failure provides ForgeX failure taxonomy loading, error classification
// and stable fingerprinting for the M4 milestone.
package failure

// Taxonomy is the set of failure classification rules loaded from YAML.
type Taxonomy struct {
	Version int           `json:"version" yaml:"version"`
	Rules   []FailureRule `json:"rules" yaml:"rules"`
}

// FailureRule classifies a matching ErrorEnvelope into a category/severity and
// records whether the failure is retryable along with a recommended action.
type FailureRule struct {
	ID             string    `json:"id" yaml:"id"`
	Category       string    `json:"category" yaml:"category"`
	Severity       string    `json:"severity" yaml:"severity"`
	Retryable      bool      `json:"retryable" yaml:"retryable"`
	Source         string    `json:"source" yaml:"source"`
	Match          RuleMatch `json:"match" yaml:"match"`
	Recommendation string    `json:"recommendation" yaml:"recommendation"`
}

// RuleMatch declares the (case-insensitive) substring conditions a rule needs.
//
// MessageContains entries must all be present in the error message. When
// OperationContains or SourceContains is non-empty it must also match.
type RuleMatch struct {
	MessageContains   []string `json:"message_contains,omitempty" yaml:"message_contains,omitempty"`
	OperationContains string   `json:"operation_contains,omitempty" yaml:"operation_contains,omitempty"`
	SourceContains    string   `json:"source_contains,omitempty" yaml:"source_contains,omitempty"`
}
