package database

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxRows limits the number of rows returned from PG queries.
	MaxRows = 100
	// QueryTimeout is the maximum execution time for a single query.
	QueryTimeout = 30 * time.Second
)

// forbiddenKeyword lists the statements the read-only worker must never
// pass through, anchored to word boundaries (see queryPG for why).
var forbiddenKeyword = regexp.MustCompile(
	`\b(INSERT|UPDATE|DELETE|DROP|ALTER|TRUNCATE|CREATE|GRANT|REVOKE)\b`)

// PGConnector abstracts PostgreSQL operations for testability.
type PGConnector interface {
	Query(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error)
	Close() error
}

// QueryResult holds the result of a PG query.
type QueryResult struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"row_count"`
}

// Worker is the Database workflow worker.
type Worker struct {
	config *Config
	pg     PGConnector
}

// NewWorker creates a Database Worker with the given configuration.
func NewWorker(cfg *Config, pg PGConnector) *Worker {
	return &Worker{
		config: cfg,
		pg:     pg,
	}
}

// Execute runs a database action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "query_pg":
		return w.queryPG(ctx, params)
	default:
		return "", fmt.Errorf("database worker: unknown action %q", action)
	}
}

// queryPG executes a read-only SQL query against PostgreSQL.
func (w *Worker) queryPG(ctx context.Context, params map[string]any) (string, error) {
	if w.config.Postgres == nil {
		return "", fmt.Errorf("database worker: postgres not configured")
	}
	if w.pg == nil {
		return "", fmt.Errorf("database worker: pg connector not initialized")
	}

	sql, _ := params["sql"].(string)
	if sql == "" {
		return "", fmt.Errorf("database worker: 'sql' parameter required")
	}

	// Shared read-only contract (see guard.go) — the agent's data.query tool
	// guards with the same function.
	sql, err := GuardReadOnlySQL(sql)
	if err != nil {
		return "", fmt.Errorf("database worker: %w", err)
	}

	// Query with timeout
	queryCtx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()

	var args []any
	if rawArgs, ok := params["args"]; ok {
		if argSlice, ok := rawArgs.([]any); ok {
			args = argSlice
		}
	}

	result, err := w.pg.Query(queryCtx, w.config.Postgres.DSN(), sql, args)
	if err != nil {
		return "", fmt.Errorf("database worker: query failed: %w", err)
	}

	// Truncate if over limit
	if len(result.Rows) > MaxRows {
		result.Rows = result.Rows[:MaxRows]
		result.RowCount = MaxRows
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("database worker: marshal result: %w", err)
	}
	return string(out), nil
}

// firstWord returns the first whitespace-delimited word of s.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i > 0 {
		return s[:i]
	}
	return s
}
