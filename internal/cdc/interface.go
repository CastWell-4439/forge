// Package cdc implements Change Data Capture for Forge.
// It monitors database changes and triggers workflows automatically.
package cdc

import (
	"context"
	"encoding/json"
	"time"
)

// Operation represents the type of data change.
type Operation string

const (
	OpInsert Operation = "INSERT"
	OpUpdate Operation = "UPDATE"
	OpDelete Operation = "DELETE"
)

// Event represents a single data change event captured from a source.
type Event struct {
	Table     string
	Operation Operation
	OldData   map[string]interface{} // nil for INSERT
	NewData   map[string]interface{} // nil for DELETE
	Timestamp time.Time
	LSN       uint64 // Log Sequence Number (PG) or position
	Raw       json.RawMessage
}

// Source defines the interface for CDC data sources.
// Each implementation (PG, MySQL, Redis, Kafka) must implement this interface.
type Source interface {
	// Subscribe starts listening for data changes and calls handler for each event.
	// It blocks until ctx is cancelled or an error occurs.
	Subscribe(ctx context.Context, handler func(Event)) error

	// Close releases all resources held by the source.
	Close() error
}

// SourceConfig is the common configuration for CDC sources.
type SourceConfig struct {
	Type        string // "postgres", "mysql", "redis", "kafka"
	Table       string
	Events      []Operation
	Filter      string // SQL-like filter expression
	Publication string // PG-specific
	SlotName    string // PG-specific
}

// MatchesFilter checks whether an event passes the configured event type filter.
func (c *SourceConfig) MatchesEvent(event Event) bool {
	if len(c.Events) == 0 {
		return true // no filter = match all
	}
	for _, op := range c.Events {
		if op == event.Operation {
			return true
		}
	}
	return false
}
