package workers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/castwell/forge/internal/workers/database"
)

// Data handler tool definition: data.query

const (
	dataQueryTimeout = 30 * time.Second
	dataQueryMaxRows = 100
)

func DataQueryDef() *ToolDef {
	return &ToolDef{
		Name:        "data.query",
		DisplayName: "Data Query",
		Category:    "data",
		Description: "Execute a read-only SQL query against the configured database. Returns rows as JSON.",
		InputSchema: map[string]ParamDef{
			"sql":    {Type: "string", Description: "SQL query to execute", Required: true},
			"params": {Type: "array", Description: "Query parameters for prepared statement"},
		},
		OutputSchema: map[string]ParamDef{
			"rows":      {Type: "array", Description: "Result rows as array of objects"},
			"columns":   {Type: "array", Description: "Column names"},
			"row_count": {Type: "integer", Description: "Number of rows returned"},
		},
		RequiredParams: []string{"sql"},
		EstimatedTime:  3 * time.Second,
	}
}

// --- Handlers ---

func NewDataQueryHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockDataQuery()
	}
	return realDataQuery(cfg)
}

func mockDataQuery() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		sql, _ := params["sql"].(string)
		if sql == "" {
			return nil, fmt.Errorf("data.query: missing required param 'sql'")
		}
		return map[string]interface{}{
			"rows": []map[string]interface{}{
				{"id": 1, "name": "mock_row_1"},
				{"id": 2, "name": "mock_row_2"},
			},
			"columns":   []string{"id", "name"},
			"row_count": 2,
		}, nil
	}
}

// realDataQuery runs the query against HandlerConfig.DataSource. The pool is
// built lazily on first use (a configured-but-unreachable database fails the
// node that needed it, with the connection error — same philosophy as the
// database worker's connector), and the read-only guard is the SHARED one
// from internal/workers/database: one SQL contract for both consumers.
func realDataQuery(cfg HandlerConfig) HandlerFunc {
	if cfg.DataSource == "" {
		return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return nil, fmt.Errorf("data.query: %w (no DataSource configured)", ErrNotConfigured)
		}
	}

	var (
		once    sync.Once
		pool    *pgxpool.Pool
		poolErr error
	)
	getPool := func(ctx context.Context) (*pgxpool.Pool, error) {
		once.Do(func() {
			p, err := pgxpool.New(ctx, cfg.DataSource)
			if err != nil {
				poolErr = fmt.Errorf("connect: %w", err)
				return
			}
			if err := p.Ping(ctx); err != nil {
				p.Close()
				poolErr = fmt.Errorf("ping: %w", err)
				return
			}
			pool = p
		})
		return pool, poolErr
	}

	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		sql, _ := params["sql"].(string)
		if sql == "" {
			return nil, fmt.Errorf("data.query: missing required param 'sql'")
		}
		guarded, err := database.GuardReadOnlySQL(sql)
		if err != nil {
			return nil, fmt.Errorf("data.query: %w", err)
		}

		queryCtx, cancel := context.WithTimeout(ctx, dataQueryTimeout)
		defer cancel()

		p, err := getPool(queryCtx)
		if err != nil {
			return nil, fmt.Errorf("data.query: %w", err)
		}

		var args []any
		if raw, ok := params["params"].([]interface{}); ok {
			args = raw
		}
		rows, err := p.Query(queryCtx, guarded, args...)
		if err != nil {
			return nil, fmt.Errorf("data.query: %w", err)
		}
		defer rows.Close()

		descs := rows.FieldDescriptions()
		columns := make([]string, len(descs))
		for i, d := range descs {
			columns[i] = d.Name
		}

		var (
			out   []map[string]interface{}
			count int
		)
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return nil, fmt.Errorf("data.query: scan: %w", err)
			}
			row := make(map[string]interface{}, len(descs))
			for i, d := range descs {
				row[d.Name] = values[i]
			}
			out = append(out, row)
			count++
			if count >= dataQueryMaxRows {
				break
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("data.query: %w", err)
		}
		if out == nil {
			out = []map[string]interface{}{}
		}
		return map[string]interface{}{
			"rows":      out,
			"columns":   columns,
			"row_count": count,
		}, nil
	}
}
