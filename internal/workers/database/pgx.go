package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout bounds the first connection attempt. A wrong DSN must
// surface as an actionable error on the node that needed the database, not
// as a request that hangs until the caller's deadline.
const connectTimeout = 5 * time.Second

// PoolConnector is the production PGConnector, backed by pgxpool.
//
// The interface hands over a DSN per call because the worker's config owns
// the connection string, while a pool is expensive to build — so pools are
// created lazily per DSN and cached. A DSN that fails to connect is not
// cached: the next call retries and reports the failure again instead of
// remembering a broken state.
//
// Values are normalised to JSON-safe forms on the way out (times as
// RFC 3339, bytea as text, uuid as its canonical string); types without a
// native mapping render as text — an honest string beats a marshal failure.
type PoolConnector struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

// NewPoolConnector creates a connector with no connections yet: nothing is
// dialled until the first query, so a worker can start without its database.
func NewPoolConnector() *PoolConnector {
	return &PoolConnector{pools: map[string]*pgxpool.Pool{}}
}

// pool returns the cached pool for dsn, creating and pinging it on first use.
func (c *PoolConnector) pool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if p, ok := c.pools[dsn]; ok {
		return p, nil
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	c.pools[dsn] = pool
	return pool, nil
}

// Query runs sql and materialises the result as columns plus JSON-safe rows.
// Execution time is the caller's to bound (the worker wraps it in its own
// timeout); this method only refuses to start with an unusable DSN.
func (c *PoolConnector) Query(ctx context.Context, dsn, sql string, args []any) (*QueryResult, error) {
	pool, err := c.pool(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	result := &QueryResult{Columns: columnNames(rows.FieldDescriptions())}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		row := make([]any, len(values))
		for i, v := range values {
			row[i] = jsonValue(v)
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

// Close releases every pool this connector created.
func (c *PoolConnector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for dsn, pool := range c.pools {
		pool.Close()
		delete(c.pools, dsn)
	}
	return nil
}

var _ PGConnector = (*PoolConnector)(nil)

// columnNames extracts names from field descriptions in column order.
func columnNames(descs []pgconn.FieldDescription) []string {
	names := make([]string, len(descs))
	for i, d := range descs {
		names[i] = d.Name
	}
	return names
}

// jsonValue converts one scanned value into something encoding/json accepts.
func jsonValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case []byte:
		return string(t)
	case [16]byte:
		// pgx hands uuid columns back as [16]byte; bytea arrives as []byte
		// above, so this shape is the uuid codec's.
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case int64, int32, int16, int, float64, float32, bool, string:
		return v
	default:
		// numeric, arrays, composites, anything else: render as text.
		return fmt.Sprintf("%v", t)
	}
}
