//go:build integration

package test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/castwell/forge/internal/cdc"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Standard connection for DML operations.
	testDSN = "postgres://xxh:781103@localhost:5432/ai_video_producer"
	// Replication connection for WAL streaming.
	testReplDSN = "postgres://xxh:781103@localhost:5432/ai_video_producer?replication=database"
	testTable   = "forge_cdc_test"
	testPub     = "forge_pub"
	testSlot    = "forge_cdc_integration_test"
)

func TestWALSource_Integration(t *testing.T) {
	ctx := context.Background()

	// Clean up: drop slot if leftover from previous run.
	cleanupSlot(t, ctx)

	// Truncate test table.
	conn, err := pgx.Connect(ctx, testDSN)
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, fmt.Sprintf("TRUNCATE %s", testTable))
	require.NoError(t, err)

	// Create WAL source.
	src := cdc.NewPGWALSource(testReplDSN,
		cdc.SourceConfig{
			Table:    testTable,
			Events:   []cdc.Operation{cdc.OpInsert, cdc.OpUpdate, cdc.OpDelete},
			SlotName: testSlot,
		},
		testPub,
		cdc.WithAutoCreateSlot(true),
		cdc.WithStandbyInterval(2*time.Second),
	)

	// Collect events in a goroutine.
	var mu sync.Mutex
	var events []cdc.Event

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	subDone := make(chan error, 1)
	go func() {
		subDone <- src.Subscribe(subCtx, func(e cdc.Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		})
	}()

	// Give the subscriber time to connect and start streaming.
	time.Sleep(2 * time.Second)

	// --- INSERT ---
	_, err = conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s (name, status) VALUES ('alice', 'active')", testTable))
	require.NoError(t, err)

	// --- UPDATE ---
	_, err = conn.Exec(ctx, fmt.Sprintf("UPDATE %s SET status = 'done' WHERE name = 'alice'", testTable))
	require.NoError(t, err)

	// --- DELETE ---
	_, err = conn.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE name = 'alice'", testTable))
	require.NoError(t, err)

	// Wait for events to arrive.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for events, got %d", n)
		case <-time.After(200 * time.Millisecond):
		}
	}

	// Stop subscriber.
	subCancel()
	<-subDone

	// Verify events.
	mu.Lock()
	defer mu.Unlock()

	require.GreaterOrEqual(t, len(events), 3)

	// INSERT
	assert.Equal(t, cdc.OpInsert, events[0].Operation)
	assert.Equal(t, testTable, events[0].Table)
	assert.Equal(t, "alice", events[0].NewData["name"])
	assert.Equal(t, "active", events[0].NewData["status"])

	// UPDATE
	assert.Equal(t, cdc.OpUpdate, events[1].Operation)
	assert.Equal(t, "done", events[1].NewData["status"])

	// DELETE
	assert.Equal(t, cdc.OpDelete, events[2].Operation)

	// Cleanup.
	require.NoError(t, src.Close())
	cleanupSlot(t, ctx)
}

func cleanupSlot(t *testing.T, ctx context.Context) {
	t.Helper()
	conn, err := pgx.Connect(ctx, testDSN)
	if err != nil {
		return
	}
	defer conn.Close(ctx)
	// Drop slot if exists (ignore errors).
	_, _ = conn.Exec(ctx, fmt.Sprintf("SELECT pg_drop_replication_slot('%s')", testSlot))
}
