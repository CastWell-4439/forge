package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// This file runs only where a real PostgreSQL exists: CI's test job ships a
// postgres:17 service and sets FORGE_PG_DSN. Locally the test skips — the
// read-only guard, config loading and failure paths are covered by unit
// tests that need no database.

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(EnvPGDSN)
	if dsn == "" {
		t.Skipf("%s not set; integration test runs in CI with the postgres service", EnvPGDSN)
	}
	return dsn
}

// TestPoolConnectorAgainstRealPostgres exercises the full path: connector
// round trip with typed values, the worker's read-only guard, the auto-LIMIT
// actually enforced by the database, and actionable failure on a bad DSN.
func TestPoolConnectorAgainstRealPostgres(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	c := NewPoolConnector()
	defer c.Close()

	// Fixture setup goes through the connector directly: the read-only guard
	// belongs to the worker's query path, not to the transport.
	if _, err := c.Query(ctx, dsn,
		`CREATE TABLE IF NOT EXISTS pg_fixture (id int primary key, label text, ts timestamptz)`, nil); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer func() {
		if _, err := c.Query(ctx, dsn, `DROP TABLE IF EXISTS pg_fixture`, nil); err != nil {
			t.Logf("cleanup fixture: %v", err)
		}
	}()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := c.Query(ctx, dsn,
		`INSERT INTO pg_fixture (id, label, ts) VALUES ($1, $2, $3)`,
		[]any{1, "hello", now}); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	// Typed round trip: int, text and timestamptz all come back JSON-safe.
	res, err := c.Query(ctx, dsn, `SELECT id, label, ts FROM pg_fixture WHERE id = $1`, []any{1})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if res.RowCount != 1 || len(res.Columns) != 3 {
		t.Fatalf("got %d rows, columns %v", res.RowCount, res.Columns)
	}
	row := res.Rows[0]
	if fmt.Sprint(row[0]) != "1" {
		t.Errorf("id = %v (%T), want 1", row[0], row[0])
	}
	if row[1] != "hello" {
		t.Errorf("label = %v, want hello", row[1])
	}
	if ts, ok := row[2].(string); !ok {
		t.Errorf("ts = %T, want an RFC 3339 string", row[2])
	} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("ts = %q, not RFC 3339: %v", ts, err)
	}

	// The worker path: config from env, guard in front, auto-LIMIT enforced
	// by the database itself (generate_series would return 500 rows).
	w := NewWorker(ConfigFromEnv(), c)
	out, err := w.Execute(ctx, "query_pg", map[string]any{
		"sql": "SELECT generate_series(1, 500) AS n",
	})
	if err != nil {
		t.Fatalf("worker query: %v", err)
	}
	var qr QueryResult
	if err := json.Unmarshal([]byte(out), &qr); err != nil {
		t.Fatalf("worker output is not JSON: %v", err)
	}
	if qr.RowCount != MaxRows {
		t.Errorf("auto-LIMIT: got %d rows, want %d", qr.RowCount, MaxRows)
	}

	// A configured worker runs an ordinary read over the fixture.
	if _, err := w.Execute(ctx, "query_pg", map[string]any{
		"sql": "SELECT id FROM pg_fixture ORDER BY id",
	}); err != nil {
		t.Errorf("ordinary read must pass the guard: %v", err)
	}
}

// A DSN pointing at a database that does not exist fails fast with a
// message naming the cause, and the broken pool is not cached — this is the
// "fail on the node that needed it, with something actionable" behaviour.
func TestPoolConnectorReportsConnectionFailure(t *testing.T) {
	integrationDSN(t) // same gating: needs a PG to be meaningful
	ctx := context.Background()
	c := NewPoolConnector()
	defer c.Close()

	bad := "postgres://forge:forge@127.0.0.1:1/forge_missing?sslmode=disable&connect_timeout=1"
	_, err := c.Query(ctx, bad, "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected a connection error for a closed port")
	}
	if !strings.Contains(err.Error(), "postgres:") {
		t.Errorf("error = %v, want it prefixed with the failing component", err)
	}
}
