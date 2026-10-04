package workers

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without a data source the tool answers honestly instead of returning mock
// rows that would look like real data.
func TestDataSourceRequiresConfiguration(t *testing.T) {
	h := NewDataQueryHandler(HandlerConfig{Mode: HandlerModeReal})
	_, err := h(context.Background(), map[string]interface{}{"sql": "SELECT 1"})
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestDataQueryMockModeCanned(t *testing.T) {
	h := NewDataQueryHandler(HandlerConfig{Mode: HandlerModeMock})
	out, err := h(context.Background(), map[string]interface{}{"sql": "SELECT 1"})
	require.NoError(t, err)
	assert.Equal(t, 2, out["row_count"])
}

// The shared read-only guard runs before any connection is attempted:
// a configured-but-violated query fails on the contract, not on the network.
func TestDataQueryEnforcesReadOnlyBeforeConnecting(t *testing.T) {
	h := NewDataQueryHandler(HandlerConfig{
		Mode:       HandlerModeReal,
		DataSource: "postgres://127.0.0.1:1/nope", // unreachable: guard must fire first
	})

	_, err := h(context.Background(), map[string]interface{}{"sql": "DELETE FROM users"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only SELECT")

	// A write keyword inside a SELECT trips too — shared with the database
	// worker's contract, one rule for both.
	_, err = h(context.Background(), map[string]interface{}{"sql": "SELECT * FROM users FOR UPDATE"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden keyword")
}

// The live path against a real database — CI's PostgreSQL service. Local
// machines without FORGE_PG_DSN skip.
func TestDataQueryAgainstRealPostgres(t *testing.T) {
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	h := NewDataQueryHandler(HandlerConfig{Mode: HandlerModeReal, DataSource: dsn})

	out, err := h(context.Background(), map[string]interface{}{"sql": "SELECT 1 AS one, 'x' AS label"})
	require.NoError(t, err)
	assert.Equal(t, 1, out["row_count"])
	rows := out["rows"].([]map[string]interface{})
	require.Len(t, rows, 1)
	assert.Equal(t, "x", rows[0]["label"])

	// The auto-LIMIT is applied by the guard and honoured by the database.
	out, err = h(context.Background(), map[string]interface{}{"sql": "SELECT generate_series(1, 500) AS n"})
	require.NoError(t, err)
	assert.Equal(t, 100, out["row_count"])
}
