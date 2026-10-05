package cdc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedQuery answers queries from a queue and records what was asked.
type scriptedQuery struct {
	answers [][]map[string]interface{}
	queries []string
	calls   int
}

func (s *scriptedQuery) query(_ context.Context, q string) ([]map[string]interface{}, error) {
	s.queries = append(s.queries, q)
	if s.calls >= len(s.answers) {
		return nil, nil
	}
	a := s.answers[s.calls]
	s.calls++
	return a, nil
}

func pollCfg() SourceConfig {
	return SourceConfig{Type: "postgres", Table: "items", CursorColumn: "created_at"}
}

// runSubscribe runs Subscribe until the context expires and returns the
// delivered events; the deadline is the normal way a test stops the loop.
func runSubscribe(t *testing.T, src Source, d time.Duration, collect *[]Event) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	err := src.Subscribe(ctx, func(e Event) { *collect = append(*collect, e) })
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// The fallback must never need logical decoding: the query is plain SQL
// against the table with a cursor, an ORDER BY and a LIMIT.
func TestPollingSourceQueriesPlainSQL(t *testing.T) {
	q := &scriptedQuery{answers: [][]map[string]interface{}{
		{{"id": 1, "created_at": time.Now().UTC().Add(2 * time.Second)}},
	}}
	src, err := NewPGPollingSource(q.query, pollCfg())
	require.NoError(t, err)

	var got []Event
	require.NoError(t, runSubscribe(t, src, 1500*time.Millisecond, &got))

	require.NotEmpty(t, q.queries)
	query := q.queries[0]
	assert.Contains(t, query, "SELECT * FROM items")
	assert.Contains(t, query, "WHERE created_at > '")
	assert.Contains(t, query, "ORDER BY created_at ASC")
	assert.Contains(t, query, "LIMIT")
	assert.NotContains(t, query, "pg_logical_slot", "polling must not touch logical decoding")

	require.Len(t, got, 1)
	assert.Equal(t, OpInsert, got[0].Operation, "row polling can only honestly claim INSERT")
	assert.Equal(t, "items", got[0].Table)
	assert.Equal(t, 1, got[0].NewData["id"])
}

// The cursor starts at construction time (no history replay) and advances to
// the last processed row's timestamp.
func TestPollingSourceCursorAdvance(t *testing.T) {
	q := &scriptedQuery{}
	src, err := NewPGPollingSource(q.query, pollCfg())
	require.NoError(t, err)

	// Round 1: nothing newer than "now" — cursor stays put.
	_, next, err := src.pollOnce(context.Background(), src.initTime)
	require.NoError(t, err)
	assert.True(t, next.Equal(src.initTime), "an empty round must not move the cursor")

	// Round 2 with a row stamped ahead of the init time (the fake ignores WHERE):
	// the cursor lands on the row's timestamp.
	rowTime := time.Now().UTC().Add(2 * time.Second)
	q2 := &scriptedQuery{answers: [][]map[string]interface{}{
		{{"created_at": rowTime}},
	}}
	src2, err := NewPGPollingSource(q2.query, pollCfg())
	require.NoError(t, err)
	_, next3, err := src2.pollOnce(context.Background(), src2.initTime)
	require.NoError(t, err)
	assert.True(t, next3.After(src2.initTime), "a processed row advances the cursor")
	assert.True(t, next3.Equal(rowTime), "the cursor lands on the row's timestamp")

	// The next query is issued from that cursor, not from scratch.
	_, _, err = src2.pollOnce(context.Background(), next3)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(q2.queries), 2)
	assert.Contains(t, q2.queries[1], rowTime.Format("2006-01-02 15:04:05.999999Z07:00"),
		"the second query starts where the first ended")
}

// A trigger asking for UPDATE/DELETE gets no events — row polling cannot see
// them — and says so up front rather than quietly never firing.
func TestPollingSourceFiltersNonInsertRequests(t *testing.T) {
	cfg := pollCfg()
	cfg.Events = []Operation{OpUpdate}
	q := &scriptedQuery{answers: [][]map[string]interface{}{
		{{"created_at": time.Now().UTC().Add(2 * time.Second)}},
	}}
	src, err := NewPGPollingSource(q.query, cfg)
	require.NoError(t, err)

	var got []Event
	require.NoError(t, runSubscribe(t, src, 1500*time.Millisecond, &got))
	assert.Empty(t, got, "an UPDATE-only trigger must not receive synthesized INSERTs")
}

// The SQL-like row filter still applies to synthesized rows.
func TestPollingSourceAppliesRowFilter(t *testing.T) {
	cfg := pollCfg()
	cfg.Filter = "status = 'new'"
	q := &scriptedQuery{answers: [][]map[string]interface{}{
		{{"created_at": time.Now().UTC().Add(2 * time.Second), "status": "archived"}},
	}}
	src, err := NewPGPollingSource(q.query, cfg)
	require.NoError(t, err)

	var got []Event
	require.NoError(t, runSubscribe(t, src, 1500*time.Millisecond, &got))
	assert.Empty(t, got, "the configured filter rejects the row")
}

// Hostile or malformed identifiers are refused before a query is built.
func TestPollingSourceValidatesIdentifiers(t *testing.T) {
	q := &scriptedQuery{}
	_, err := NewPGPollingSource(q.query, SourceConfig{Table: "items; DROP TABLE users"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid table name")

	_, err = NewPGPollingSource(q.query, SourceConfig{Table: "items", CursorColumn: "created_at; --"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cursor column")

	_, err = NewPGPollingSource(nil, pollCfg())
	require.Error(t, err)
}

// A cursor cell that cannot be read as time is skipped, not fatal — the feed
// keeps running instead of stalling on one bad row.
func TestPollingSourceSkipsUnparseableCursor(t *testing.T) {
	q := &scriptedQuery{answers: [][]map[string]interface{}{
		{{"created_at": 42}, {"created_at": time.Now().UTC().Add(2 * time.Second)}},
	}}
	src, err := NewPGPollingSource(q.query, pollCfg())
	require.NoError(t, err)

	var got []Event
	require.NoError(t, runSubscribe(t, src, 1500*time.Millisecond, &got))
	assert.Len(t, got, 1, "the row with a usable cursor is delivered; the other skipped")
}

// --- capability classifier ---

func TestCapabilityErrorClassification(t *testing.T) {
	capability := []string{
		"ERROR: permission denied to use logical replication (SQLSTATE 42501)",
		"ERROR: replication slots are not supported (SQLSTATE 0A000)",
		"ERROR: replication slot \"cdc_items\" does not exist (SQLSTATE 42704)",
		"must be superuser to use replication slots",
		"server does not support logical replication wal_level=replica",
	}
	for _, msg := range capability {
		assert.True(t, CapabilityError(errors.New(msg)), "capability: %q", msg)
	}

	transient := []string{
		"dial tcp 10.0.0.5:5432: connection refused",
		"driver: bad connection",
		"syntax error at or near \"SELCT\"",
		"context deadline exceeded",
	}
	for _, msg := range transient {
		assert.False(t, CapabilityError(errors.New(msg)), "transient must not downgrade: %q", msg)
	}
	assert.False(t, CapabilityError(nil))
}

// --- fallback wrapper ---

type fakeSource struct {
	err error
	ran bool
}

func (f *fakeSource) Subscribe(_ context.Context, _ func(Event)) error {
	f.ran = true
	return f.err
}
func (f *fakeSource) Close() error { return nil }

// The promise: WAL unavailable (capability) downgrades to polling on the
// same handler, automatically.
func TestFallbackDowngradesOnCapabilityError(t *testing.T) {
	primary := &fakeSource{err: errors.New("permission denied to use logical replication (SQLSTATE 42501)")}
	fallback := &fakeSource{}

	err := NewFallbackSource("items", primary, fallback).Subscribe(context.Background(), func(Event) {})
	require.NoError(t, err, "the downgrade itself is not an error")
	assert.True(t, primary.ran)
	assert.True(t, fallback.ran, "polling took over")
}

// A transient failure must NOT downgrade — polling would hide the real fault.
func TestFallbackDoesNotHideTransientErrors(t *testing.T) {
	primary := &fakeSource{err: errors.New("connection refused")}
	fallback := &fakeSource{}

	err := NewFallbackSource("items", primary, fallback).Subscribe(context.Background(), func(Event) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not downgrading")
	assert.False(t, fallback.ran, "polling must not silently take over")
}

// A cancelled context is not a capability failure and does not downgrade.
func TestFallbackIgnoresCancellation(t *testing.T) {
	primary := &fakeSource{err: context.Canceled}
	fallback := &fakeSource{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewFallbackSource("items", primary, fallback).Subscribe(ctx, func(Event) {})
	require.Error(t, err)
	assert.False(t, fallback.ran)
}
