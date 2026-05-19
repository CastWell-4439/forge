// Package scheduler implements the workflow trigger system including polling,
// deduplication, and scheduled workflow instantiation.
package scheduler

import (
	"context"
	"time"
)

// Event represents a trigger event that may start a workflow instance.
type Event struct {
	ID      string         // unique event identifier (used for dedup)
	Payload map[string]any // event data passed to workflow inputs
}

// Trigger is the interface for all workflow trigger types.
type Trigger interface {
	// Name returns the unique trigger identifier.
	Name() string
	// Check polls for new events and returns any that should trigger a workflow.
	Check(ctx context.Context) ([]Event, error)
	// Interval returns how often this trigger should be checked.
	Interval() time.Duration
	// WorkflowName returns the workflow this trigger is bound to.
	WorkflowName() string
}

// PollTrigger implements a polling-based trigger that periodically queries a source.
type PollTrigger struct {
	name         string
	interval     time.Duration
	source       string // e.g. "feishu_mcp"
	query        string
	dedupKey     string // template for generating event ID
	workflowName string
	pollFn       PollFunc
}

// PollFunc is the function that performs the actual polling.
// Implementations query external systems and return discovered events.
type PollFunc func(ctx context.Context, source, query string) ([]Event, error)

// PollTriggerConfig configures a PollTrigger.
type PollTriggerConfig struct {
	Name         string
	Interval     time.Duration
	Source       string
	Query        string
	DedupKey     string
	WorkflowName string
	PollFn       PollFunc
}

// NewPollTrigger creates a new polling trigger.
func NewPollTrigger(cfg PollTriggerConfig) *PollTrigger {
	return &PollTrigger{
		name:         cfg.Name,
		interval:     cfg.Interval,
		source:       cfg.Source,
		query:        cfg.Query,
		dedupKey:     cfg.DedupKey,
		workflowName: cfg.WorkflowName,
		pollFn:       cfg.PollFn,
	}
}

func (t *PollTrigger) Name() string           { return t.name }
func (t *PollTrigger) Interval() time.Duration { return t.interval }
func (t *PollTrigger) WorkflowName() string    { return t.workflowName }
func (t *PollTrigger) Source() string          { return t.source }
func (t *PollTrigger) Query() string           { return t.query }

// Check calls the poll function to discover new events.
func (t *PollTrigger) Check(ctx context.Context) ([]Event, error) {
	if t.pollFn == nil {
		return nil, nil
	}
	return t.pollFn(ctx, t.source, t.query)
}
