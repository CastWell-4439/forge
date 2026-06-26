package model

import "time"

// EventType is the canonical type of a ForgeX event.
type EventType string

const (
	EventRunStarted      EventType = "run_started"
	EventStepStarted     EventType = "step_started"
	EventToolCalled      EventType = "tool_called"
	EventToolSucceeded   EventType = "tool_succeeded"
	EventToolFailed      EventType = "tool_failed"
	EventStopDecided     EventType = "stop_decided"
	EventReportGenerated EventType = "report_generated"
	EventRunFinished     EventType = "run_finished"
)

// Event is a JSONL-friendly audit event for a ForgeX run.
type Event struct {
	ID        string         `json:"id" yaml:"id"`
	RunID     string         `json:"run_id" yaml:"run_id"`
	Type      EventType      `json:"type" yaml:"type"`
	Message   string         `json:"message" yaml:"message"`
	Timestamp time.Time      `json:"timestamp" yaml:"timestamp"`
	Data      map[string]any `json:"data,omitempty" yaml:"data,omitempty"`
}
