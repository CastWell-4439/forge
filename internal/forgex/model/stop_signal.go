package model

import "time"

// StopSignalRecord is the persisted representation of a termination signal.
type StopSignalRecord struct {
	ID        string     `json:"id" yaml:"id"`
	RunID     string     `json:"run_id" yaml:"run_id"`
	Source    string     `json:"source" yaml:"source"`
	Severity  string     `json:"severity" yaml:"severity"`
	Suggested StopAction `json:"suggested" yaml:"suggested"`
	Reason    string     `json:"reason" yaml:"reason"`
	Evidence  []string   `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	CreatedAt time.Time  `json:"created_at" yaml:"created_at"`
}
