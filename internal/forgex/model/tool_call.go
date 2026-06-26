package model

import "time"

// ToolCall captures one tool invocation attempt and its result or error.
type ToolCall struct {
	ID        string         `json:"id" yaml:"id"`
	RunID     string         `json:"run_id" yaml:"run_id"`
	ToolName  string         `json:"tool_name" yaml:"tool_name"`
	Args      map[string]any `json:"args,omitempty" yaml:"args,omitempty"`
	Result    map[string]any `json:"result,omitempty" yaml:"result,omitempty"`
	Error     string         `json:"error,omitempty" yaml:"error,omitempty"`
	StartedAt time.Time      `json:"started_at" yaml:"started_at"`
	EndedAt   time.Time      `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
}
