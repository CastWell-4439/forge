package bus

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The PG LISTEN/NOTIFY bus had only close-semantics tests — nothing proved
// an event actually travels from Publish to Subscribe. This runs against
// CI's PostgreSQL service (same env gate as the other PG integration tests).
func TestPGNotifyRoundTrip(t *testing.T) {
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	b := NewPGNotifyBus(pool)
	defer b.Close()

	ch, err := b.Subscribe(ctx, "workflow.events")
	require.NoError(t, err)

	// LISTEN takes effect asynchronously; publish until it lands.
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		require.NoError(t, b.Publish(ctx, "workflow.events", `{"type":"WORKFLOW_STARTED"}`))
		select {
		case payload := <-ch:
			require.Contains(t, payload, "WORKFLOW_STARTED")
			return
		case <-deadline:
			t.Fatal("timed out waiting for the notified event")
		case <-ticker.C:
		}
	}
}
