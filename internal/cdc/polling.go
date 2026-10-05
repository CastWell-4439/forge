package cdc

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"time"
)

// The true polling fallback: plain SQL against the table, no logical decoding.
//
// README promises "轮询回退：WAL 不可用时（权限不足 / 版本不支持）自动降级为
// 轮询模式" — and the previous "poll" source was not that: it peeked a
// replication slot (pg_logical_slot_peek_changes), which needs exactly the
// capability that is supposedly unavailable. This source needs nothing but
// SELECT: it reads rows newer than a cursor and synthesises INSERT events.
//
// The honest trade-offs, stated where they bite:
//
//   - INSERT only. Row polling cannot tell an UPDATE from an INSERT (the old
//     row is gone by the time we read), and cannot see DELETEs at all. A
//     trigger configured for UPDATE/DELETE gets a warning and no events,
//     instead of silently never firing.
//   - The cursor starts at construction time: a fallback does not replay the
//     table's history. Changes made while the poller itself was down are
//     missed — the alternative (scan from zero) re-fires the whole table into
//     workflows on every restart, which is worse.
//   - In-memory cursor: a restart starts again from "now". This is best-effort
//     degradation, not exactly-once delivery.

const pollBatchLimit = 1000

// tableIdentRe validates an unqualified or schema-qualified table identifier
// before it goes into a query string. The cursor value itself is a time.Time
// the database produced, formatted by us — the table name is the only piece
// that comes from configuration.
var tableIdentRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// PGPollingSource implements Source by selecting rows past a cursor.
type PGPollingSource struct {
	config   SourceConfig
	queryFn  func(ctx context.Context, query string) ([]map[string]interface{}, error)
	closed   chan struct{}
	initTime time.Time
}

// NewPGPollingSource creates a SELECT-only CDC source. The cursor column
// must be a timestamp column; rows without a parseable cursor are skipped
// with a warning rather than stalling the feed.
func NewPGPollingSource(queryFn func(ctx context.Context, query string) ([]map[string]interface{}, error), config SourceConfig) (*PGPollingSource, error) {
	if queryFn == nil {
		return nil, fmt.Errorf("pg poll: query function is required")
	}
	if !tableIdentRe.MatchString(config.Table) {
		return nil, fmt.Errorf("pg poll: invalid table name %q", config.Table)
	}
	cursor := config.CursorColumn
	if cursor == "" {
		cursor = "created_at"
	}
	if !tableIdentRe.MatchString(cursor) {
		return nil, fmt.Errorf("pg poll: invalid cursor column %q", cursor)
	}
	config.CursorColumn = cursor

	s := &PGPollingSource{
		config:   config,
		queryFn:  queryFn,
		closed:   make(chan struct{}),
		initTime: time.Now().UTC(),
	}
	return s, nil
}

// Subscribe polls the table until ctx is cancelled or Close is called.
func (s *PGPollingSource) Subscribe(ctx context.Context, handler func(Event)) error {
	// Row polling sees only new rows; say so up front instead of letting an
	// UPDATE/DELETE trigger quietly never fire.
	for _, op := range s.config.Events {
		if op != OpInsert {
			log.Printf("WARN: pg poll: table %q asks for %s events; SELECT-based polling can only detect new rows (INSERT) — %s will not trigger",
				s.config.Table, op, op)
		}
	}
	log.Printf("INFO: pg poll: watching table %q on column %q (no logical decoding required)",
		s.config.Table, s.config.CursorColumn)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	cursor := s.initTime
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		case <-ticker.C:
			events, next, err := s.pollOnce(ctx, cursor)
			if err != nil {
				log.Printf("WARN: pg poll: %v", err)
				continue
			}
			if next.After(cursor) {
				cursor = next
			}
			for _, event := range events {
				if !s.config.MatchesEvent(event) {
					continue
				}
				if !evaluateSimpleFilter(s.config.Filter, event.NewData) {
					continue
				}
				handler(event)
			}
		}
	}
}

// pollOnce reads one batch and reports the advanced cursor. The cursor only
// moves to a fully processed row's timestamp: a parse failure leaves it put
// so rows are re-read next tick instead of being skipped.
func (s *PGPollingSource) pollOnce(ctx context.Context, cursor time.Time) ([]Event, time.Time, error) {
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE %s > '%s' ORDER BY %s ASC LIMIT %d",
		s.config.Table, s.config.CursorColumn,
		cursor.UTC().Format("2006-01-02 15:04:05.999999Z07:00"),
		s.config.CursorColumn, pollBatchLimit,
	)
	rows, err := s.queryFn(ctx, query)
	if err != nil {
		return nil, cursor, fmt.Errorf("query %s: %w", s.config.Table, err)
	}

	events := make([]Event, 0, len(rows))
	next := cursor
	for _, row := range rows {
		rowCursor, ok := parseCursor(row[s.config.CursorColumn])
		if !ok {
			log.Printf("WARN: pg poll: row on %s has unusable cursor value %v; skipping",
				s.config.CursorColumn, row[s.config.CursorColumn])
			continue
		}
		events = append(events, Event{
			Table:     s.config.Table,
			Operation: OpInsert, // all a row poll can honestly claim
			NewData:   row,
			Timestamp: rowCursor,
		})
		if rowCursor.After(next) {
			next = rowCursor
		}
	}
	return events, next, nil
}

// parseCursor coerces a cursor cell to time. pgx hands timestamptz over as
// time.Time; a string is accepted when it parses, so a text column works too.
func parseCursor(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999Z07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// Close stops the polling loop.
func (s *PGPollingSource) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
