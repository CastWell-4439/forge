package database

import (
	"context"
	"strings"
	"testing"
)

// --- Configuration loading ---

// A full DSN wins over the individual variables: it is the form every
// managed Postgres hands out, and decomposing it would lose parameters.
func TestConfigFromEnvDSNWins(t *testing.T) {
	t.Setenv(EnvPGDSN, "postgres://user:pw@dbhost:6543/frob?sslmode=require")
	t.Setenv(EnvPGHost, "ignored-host")

	cfg := ConfigFromEnv()
	if cfg == nil || cfg.Postgres == nil {
		t.Fatal("expected a config")
	}
	got := cfg.Postgres.DSN()
	want := "postgres://user:pw@dbhost:6543/frob?sslmode=require"
	if got != want {
		t.Errorf("DSN() = %q, want the raw connection string %q", got, want)
	}
}

// Without a DSN the individual variables assemble into one, with the same
// 5432 default the struct path has always had.
func TestConfigFromEnvAssemblesParts(t *testing.T) {
	t.Setenv(EnvPGDSN, "")
	t.Setenv(EnvPGHost, "db.internal")
	t.Setenv(EnvPGPort, "6543")
	t.Setenv(EnvPGDB, "forge")
	t.Setenv(EnvPGUser, "runner")
	t.Setenv(EnvPGPassword, "secret")
	t.Setenv(EnvPGPasswordEnv, "")

	cfg := ConfigFromEnv()
	if cfg == nil || cfg.Postgres == nil {
		t.Fatal("expected a config")
	}
	got := cfg.Postgres.DSN()
	for _, want := range []string{"host=db.internal", "port=6543", "dbname=forge", "user=runner", "password=secret"} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN() = %q, missing %q", got, want)
		}
	}
}

// Nothing configured means nil — the caller reports "not configured" instead
// of letting a query fail on an empty connection string.
func TestConfigFromEnvNothingSetReturnsNil(t *testing.T) {
	for _, key := range []string{EnvPGDSN, EnvPGHost} {
		t.Setenv(key, "")
	}
	if cfg := ConfigFromEnv(); cfg != nil {
		t.Errorf("expected nil config, got %+v", cfg)
	}
}

// A structured config still defaults to 5432 when no port is given.
func TestPGConfigDefaultPort(t *testing.T) {
	cfg := &PGConfig{Host: "h", DB: "d", User: "u"}
	if got := cfg.DSN(); !strings.Contains(got, "port=5432") {
		t.Errorf("DSN() = %q, want port=5432 default", got)
	}
}

// --- Read-only guard ---

// The guard used to match keywords as substrings, so ordinary columns —
// updated_at, created_at — were rejected as UPDATE/CREATE. Word boundaries
// restore exactly the queries the read-only worker exists to run.
func TestQueryPGAllowsOrdinaryColumnNames(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	var capturedSQL string
	pg := &mockPGConnector{
		queryFn: func(_ context.Context, _, sql string, _ []any) (*QueryResult, error) {
			capturedSQL = sql
			return &QueryResult{Columns: []string{"updated_at"}, Rows: [][]any{{"x"}}, RowCount: 1}, nil
		},
	}
	w := NewWorker(cfg, pg)

	if _, err := w.Execute(context.Background(), "query_pg", map[string]any{
		"sql": "SELECT id, updated_at, created_at FROM audit_log WHERE created_at > now()",
	}); err != nil {
		t.Fatalf("a read-only query over timestamp columns must pass, got: %v", err)
	}
	if !strings.Contains(capturedSQL, "updated_at") {
		t.Errorf("query never reached the connector: %s", capturedSQL)
	}
}

// Whole-word matching still catches real danger, including the locking read
// FOR UPDATE — that is a write-intent clause, not a column name.
func TestQueryPGRejectsWriteIntents(t *testing.T) {
	cfg := &Config{Postgres: &PGConfig{Host: "localhost", DB: "test", User: "test"}}
	w := NewWorker(cfg, &mockPGConnector{})

	cases := []struct {
		sql  string
		want string // substring the error must contain
	}{
		{"SELECT * FROM users FOR UPDATE", "forbidden keyword"},                 // locks rows
		{"SELECT 1; DROP TABLE users;--", "forbidden keyword"},                  // multi-statement
		{"UPDATE users SET active = false", "only SELECT"},                      // not a SELECT
		{"SELECT id FROM users WHERE name = 'drop table'", "forbidden keyword"}, // conservative on literals
	}
	for _, tc := range cases {
		_, err := w.Execute(context.Background(), "query_pg", map[string]any{"sql": tc.sql})
		if err == nil {
			t.Errorf("%q: expected rejection, got nil", tc.sql)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not contain %q", tc.sql, err, tc.want)
		}
	}
}

// --- Connector: failures without a database ---

// A malformed DSN fails at parse time with a message that names the cause —
// no connection attempt, no hanging request.
func TestPoolConnectorRejectsMalformedDSN(t *testing.T) {
	c := NewPoolConnector()
	defer c.Close()

	_, err := c.Query(context.Background(), "://not-a-dsn", "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected an error for a malformed DSN")
	}
	if !strings.Contains(err.Error(), "parse DSN") {
		t.Errorf("error = %v, want it to name the parse failure", err)
	}
}

// An empty DSN is refused before any dialling.
func TestPoolConnectorRejectsEmptyDSN(t *testing.T) {
	c := NewPoolConnector()
	defer c.Close()

	if _, err := c.Query(context.Background(), "", "SELECT 1", nil); err == nil {
		t.Fatal("expected an error for an empty DSN")
	}
}

// Close releases pools and is safe to call twice (workers defer it).
func TestPoolConnectorCloseIsIdempotent(t *testing.T) {
	c := NewPoolConnector()
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
