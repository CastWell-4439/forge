package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// MaxRows limits the number of rows returned from PG queries.
	MaxRows = 100
	// QueryTimeout is the maximum execution time for a single query.
	QueryTimeout = 30 * time.Second
)

// PGConnector abstracts PostgreSQL operations for testability.
type PGConnector interface {
	Query(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error)
	Close() error
}

// RedisConnector abstracts Redis operations for testability.
type RedisConnector interface {
	Get(ctx context.Context, addr, password string, db int, key string) (string, error)
	Keys(ctx context.Context, addr, password string, db int, pattern string) ([]string, error)
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
	redis  RedisConnector
}

// NewWorker creates a Database Worker with the given configuration.
func NewWorker(cfg *Config, pg PGConnector, redis RedisConnector) *Worker {
	return &Worker{
		config: cfg,
		pg:     pg,
		redis:  redis,
	}
}

// Execute runs a database action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "query_pg":
		return w.queryPG(ctx, params)
	case "query_redis":
		return w.queryRedis(ctx, params)
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

	// Security: only SELECT allowed
	normalized := strings.TrimSpace(strings.ToUpper(sql))
	if !strings.HasPrefix(normalized, "SELECT") {
		return "", fmt.Errorf("database worker: only SELECT queries allowed, got: %s", firstWord(sql))
	}

	// Dangerous patterns
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "TRUNCATE", "CREATE", "GRANT", "REVOKE"} {
		if strings.Contains(normalized, kw) {
			return "", fmt.Errorf("database worker: query contains forbidden keyword %q", kw)
		}
	}

	// Enforce LIMIT
	if !strings.Contains(normalized, "LIMIT") {
		sql = sql + fmt.Sprintf(" LIMIT %d", MaxRows)
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

// queryRedis executes a read-only Redis command.
func (w *Worker) queryRedis(ctx context.Context, params map[string]any) (string, error) {
	if w.config.Redis == nil {
		return "", fmt.Errorf("database worker: redis not configured")
	}
	if w.redis == nil {
		return "", fmt.Errorf("database worker: redis connector not initialized")
	}

	command, _ := params["command"].(string)
	key, _ := params["key"].(string)

	addr := w.config.Redis.Addr()
	password := w.config.Redis.GetPassword()
	db := w.config.Redis.DB

	switch strings.ToUpper(command) {
	case "GET":
		if key == "" {
			return "", fmt.Errorf("database worker: 'key' required for GET")
		}
		val, err := w.redis.Get(ctx, addr, password, db, key)
		if err != nil {
			return "", fmt.Errorf("database worker: redis GET: %w", err)
		}
		result := map[string]any{"key": key, "value": val}
		out, _ := json.Marshal(result)
		return string(out), nil

	case "KEYS":
		pattern, _ := params["pattern"].(string)
		if pattern == "" {
			pattern = key
		}
		if pattern == "" {
			return "", fmt.Errorf("database worker: 'pattern' or 'key' required for KEYS")
		}
		keys, err := w.redis.Keys(ctx, addr, password, db, pattern)
		if err != nil {
			return "", fmt.Errorf("database worker: redis KEYS: %w", err)
		}
		// Limit returned keys
		if len(keys) > MaxRows {
			keys = keys[:MaxRows]
		}
		result := map[string]any{"pattern": pattern, "keys": keys, "count": len(keys)}
		out, _ := json.Marshal(result)
		return string(out), nil

	default:
		return "", fmt.Errorf("database worker: redis command %q not allowed (only GET/KEYS)", command)
	}
}

// firstWord returns the first whitespace-delimited word of s.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i > 0 {
		return s[:i]
	}
	return s
}
