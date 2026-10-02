package database

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- Mock connectors ---

type mockPGConnector struct {
	queryFn func(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error)
}

func (m *mockPGConnector) Query(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, dsn, sql, args)
	}
	return &QueryResult{Columns: []string{"id"}, Rows: [][]any{{1}}, RowCount: 1}, nil
}
func (m *mockPGConnector) Close() error { return nil }

// --- Tests ---

func TestQueryPG_SelectOnly(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	w := NewWorker(cfg, &mockPGConnector{})

	result, err := w.Execute(context.Background(), "query_pg", map[string]any{
		"sql": "SELECT id, name FROM users WHERE active = true",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var qr QueryResult
	if err := json.Unmarshal([]byte(result), &qr); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if qr.RowCount != 1 {
		t.Errorf("expected 1 row, got %d", qr.RowCount)
	}
}

func TestQueryPG_RejectInsert(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	w := NewWorker(cfg, &mockPGConnector{})

	_, err := w.Execute(context.Background(), "query_pg", map[string]any{
		"sql": "INSERT INTO users (name) VALUES ('hacker')",
	})
	if err == nil {
		t.Fatal("expected error for INSERT, got nil")
	}
	if !strings.Contains(err.Error(), "only SELECT") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestQueryPG_RejectSelectWithDrop(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	w := NewWorker(cfg, &mockPGConnector{})

	_, err := w.Execute(context.Background(), "query_pg", map[string]any{
		"sql": "SELECT 1; DROP TABLE users;--",
	})
	if err == nil {
		t.Fatal("expected error for DROP, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden keyword") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestQueryPG_AutoLimit(t *testing.T) {
	var capturedSQL string
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	pg := &mockPGConnector{
		queryFn: func(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error) {
			capturedSQL = sql
			return &QueryResult{Columns: []string{"id"}, Rows: [][]any{{1}}, RowCount: 1}, nil
		},
	}
	w := NewWorker(cfg, pg)

	_, err := w.Execute(context.Background(), "query_pg", map[string]any{
		"sql": "SELECT id FROM tasks",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedSQL, "LIMIT 100") {
		t.Errorf("expected auto LIMIT, got: %s", capturedSQL)
	}
}

func TestQueryPG_EmptySQL(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	w := NewWorker(cfg, &mockPGConnector{})

	_, err := w.Execute(context.Background(), "query_pg", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "'sql' parameter required") {
		t.Errorf("expected sql required error, got: %v", err)
	}
}

func TestQueryPG_NoPGConfig(t *testing.T) {
	w := NewWorker(&Config{}, &mockPGConnector{})

	_, err := w.Execute(context.Background(), "query_pg", map[string]any{"sql": "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected not configured error, got: %v", err)
	}
}

func TestUnknownAction(t *testing.T) {
	w := NewWorker(&Config{}, nil)
	_, err := w.Execute(context.Background(), "drop_all", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected unknown action error, got: %v", err)
	}
}
