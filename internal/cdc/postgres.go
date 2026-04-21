package cdc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// PGCDCSource implements the Source interface for PostgreSQL
// using logical replication (pgoutput protocol).
//
// Current status: POLLING-BASED FALLBACK.
// Production migration path to real WAL streaming:
//   1. Add dependency: go get github.com/jackc/pglogrepl
//   2. Replace pollChanges() with pglogrepl.StartReplication()
//   3. Create a replication slot: SELECT pg_create_logical_replication_slot('forge_cdc', 'pgoutput')
//   4. Parse pgoutput messages in a streaming loop (INSERT/UPDATE/DELETE → ChangeEvent)
//   5. Track confirmed_flush_lsn for at-least-once delivery
//   6. Remove the polling ticker and simulateChange helper
//
// The polling implementation exercises the full Source interface
// (Subscribe, GetChanges, Commit, GetLag, Snapshot) so that
// consumers don't need to change when WAL streaming is integrated.
type PGCDCSource struct {
	config   SourceConfig
	connStr  string
	closed   chan struct{}
	queryFn  func(ctx context.Context, query string) ([]map[string]interface{}, error)
}

// PGCDCOption configures a PGCDCSource.
type PGCDCOption func(*PGCDCSource)

// WithQueryFunc sets a custom query function (for testing / DI).
func WithQueryFunc(fn func(ctx context.Context, query string) ([]map[string]interface{}, error)) PGCDCOption {
	return func(s *PGCDCSource) {
		s.queryFn = fn
	}
}

// NewPGCDCSource creates a new PostgreSQL CDC source.
func NewPGCDCSource(connStr string, config SourceConfig, opts ...PGCDCOption) *PGCDCSource {
	s := &PGCDCSource{
		config:  config,
		connStr: connStr,
		closed:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Subscribe starts listening for PostgreSQL changes.
// It uses polling as a portable implementation. For production WAL streaming,
// replace the poll loop with pglogrepl.StartReplication.
func (s *PGCDCSource) Subscribe(ctx context.Context, handler func(Event)) error {
	if s.queryFn == nil {
		return fmt.Errorf("pg cdc: no query function configured (set WithQueryFunc or connect to real PG)")
	}

	log.Printf("INFO: pg-cdc: subscribing to table %q events %v", s.config.Table, s.config.Events)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastLSN uint64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		case <-ticker.C:
			events, err := s.pollChanges(ctx, lastLSN)
			if err != nil {
				log.Printf("WARN: pg-cdc: poll error: %v", err)
				continue
			}
			for _, event := range events {
				if !s.config.MatchesEvent(event) {
					continue
				}
				if !s.matchesFilter(event) {
					continue
				}
				handler(event)
				if event.LSN > lastLSN {
					lastLSN = event.LSN
				}
			}
		}
	}
}

// Close stops the CDC source.
func (s *PGCDCSource) Close() error {
	select {
	case <-s.closed:
		// already closed
	default:
		close(s.closed)
	}
	return nil
}

// pollChanges queries for new changes since lastLSN.
// This is a polling implementation. Production would use WAL streaming.
func (s *PGCDCSource) pollChanges(ctx context.Context, lastLSN uint64) ([]Event, error) {
	// Use parameterized query pattern to avoid SQL injection.
	// Note: pg_logical_slot_peek_changes doesn't support $1 for slot name,
	// so we validate the slot name contains only safe characters.
	if !isValidSlotName(s.config.SlotName) {
		return nil, fmt.Errorf("pg cdc: invalid slot name %q", s.config.SlotName)
	}

	query := fmt.Sprintf(
		"SELECT * FROM pg_logical_slot_peek_changes('%s', NULL, NULL)",
		s.config.SlotName,
	)

	rows, err := s.queryFn(ctx, query)
	if err != nil {
		return nil, err
	}

	var events []Event
	for _, row := range rows {
		event, err := s.parseRow(row)
		if err != nil {
			log.Printf("WARN: pg-cdc: parse row: %v", err)
			continue
		}
		if event.LSN > lastLSN {
			events = append(events, event)
		}
	}

	return events, nil
}

// isValidSlotName checks that a replication slot name contains only safe characters.
func isValidSlotName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// parseRow converts a raw database row into a CDC Event.
func (s *PGCDCSource) parseRow(row map[string]interface{}) (Event, error) {
	event := Event{
		Table:     s.config.Table,
		Timestamp: time.Now(),
	}

	// Parse operation type from the change data.
	if data, ok := row["data"]; ok {
		raw, err := json.Marshal(data)
		if err == nil {
			event.Raw = raw
		}
	}

	// Determine operation from change type.
	if opStr, ok := row["operation"].(string); ok {
		switch opStr {
		case "INSERT", "insert":
			event.Operation = OpInsert
		case "UPDATE", "update":
			event.Operation = OpUpdate
		case "DELETE", "delete":
			event.Operation = OpDelete
		}
	}

	// Extract new/old data.
	if newData, ok := row["new_data"].(map[string]interface{}); ok {
		event.NewData = newData
	}
	if oldData, ok := row["old_data"].(map[string]interface{}); ok {
		event.OldData = oldData
	}

	// LSN.
	if lsn, ok := row["lsn"].(uint64); ok {
		event.LSN = lsn
	}

	return event, nil
}

// matchesFilter evaluates the SQL-like filter expression against the event.
// Currently supports simple "field = 'value'" expressions.
func (s *PGCDCSource) matchesFilter(event Event) bool {
	if s.config.Filter == "" {
		return true
	}

	// Simple filter parsing: "field = 'value'"
	// Full SQL expression evaluation would use a proper parser.
	return evaluateSimpleFilter(s.config.Filter, event.NewData)
}

// evaluateSimpleFilter handles "field = 'value'" and "field != 'value'" patterns.
func evaluateSimpleFilter(filter string, data map[string]interface{}) bool {
	if data == nil {
		return false
	}

	// Parse "field = 'value'" or "field != 'value'"
	// This is intentionally simple; production would use a proper expression parser.
	var field, op, value string

	// Try "field != 'value'"
	n, _ := fmt.Sscanf(filter, "%s != '%s", &field, &value)
	if n == 2 {
		op = "!="
		// Remove trailing quote.
		if len(value) > 0 && value[len(value)-1] == '\'' {
			value = value[:len(value)-1]
		}
	} else {
		// Try "field = 'value'"
		n, _ = fmt.Sscanf(filter, "%s = '%s", &field, &value)
		if n == 2 {
			op = "="
			if len(value) > 0 && value[len(value)-1] == '\'' {
				value = value[:len(value)-1]
			}
		} else {
			// Can't parse — default to pass.
			return true
		}
	}

	actual, ok := data[field]
	if !ok {
		return false
	}

	actualStr := fmt.Sprintf("%v", actual)
	switch op {
	case "=":
		return actualStr == value
	case "!=":
		return actualStr != value
	default:
		return true
	}
}
