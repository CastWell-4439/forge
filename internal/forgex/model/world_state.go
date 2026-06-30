package model

import "time"

// StateStatus describes whether a state entry or claim is accepted as fact.
type StateStatus string

const (
	StateProposed   StateStatus = "proposed"
	StateAccepted   StateStatus = "accepted"
	StateRejected   StateStatus = "rejected"
	StateStale      StateStatus = "stale"
	StateConflicted StateStatus = "conflicted"
)

// WorldState is the explicit state snapshot for a long-running ForgeX run.
type WorldState struct {
	RunID     string       `json:"run_id" yaml:"run_id"`
	Version   int          `json:"version" yaml:"version"`
	Entries   []StateEntry `json:"entries" yaml:"entries"`
	UpdatedAt time.Time    `json:"updated_at" yaml:"updated_at"`
}

// StateEntry is one explicit state fact or flagged state record.
type StateEntry struct {
	Key        string         `json:"key" yaml:"key"`
	Value      map[string]any `json:"value" yaml:"value"`
	Status     StateStatus    `json:"status" yaml:"status"`
	Producer   string         `json:"producer" yaml:"producer"`
	Confidence float64        `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Evidence   []string       `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Version    int            `json:"version" yaml:"version"`
	Scope      string         `json:"scope,omitempty" yaml:"scope,omitempty"`
}

// StateClaim records a producer's proposed state update before or after validation.
type StateClaim struct {
	ID        string         `json:"id" yaml:"id"`
	RunID     string         `json:"run_id" yaml:"run_id"`
	Key       string         `json:"key" yaml:"key"`
	Value     map[string]any `json:"value" yaml:"value"`
	Producer  string         `json:"producer" yaml:"producer"`
	Evidence  []string       `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Status    StateStatus    `json:"status" yaml:"status"`
	Reason    string         `json:"reason,omitempty" yaml:"reason,omitempty"`
	CreatedAt time.Time      `json:"created_at" yaml:"created_at"`
}
