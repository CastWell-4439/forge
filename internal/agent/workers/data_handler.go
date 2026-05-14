package workers

import (
	"context"
	"fmt"
	"time"
)

// Data handler tool definition: data.query

func DataQueryDef() *ToolDef {
	return &ToolDef{
		Name:           "data.query",
		DisplayName:    "Data Query",
		Category:       "data",
		Description:    "Execute a SQL query against the configured database. Returns rows as JSON.",
		InputSchema: map[string]ParamDef{
			"sql":    {Type: "string", Description: "SQL query to execute", Required: true},
			"params": {Type: "array", Description: "Query parameters for prepared statement"},
		},
		OutputSchema: map[string]ParamDef{
			"rows":     {Type: "array", Description: "Result rows as array of objects"},
			"columns":  {Type: "array", Description: "Column names"},
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
	return realDataQuery()
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

func realDataQuery() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("data.query: %w", ErrNotConfigured)
	}
}
