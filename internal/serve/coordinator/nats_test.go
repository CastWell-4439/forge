package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/bus"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
)

// startNATS starts an in-process NATS server with JetStream and returns its
// client URL (embedded-server pattern from internal/bus tests).
func startNATS(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	srv.Start()
	require.True(t, srv.ReadyForConnections(5*time.Second), "NATS server not ready")
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func boltStore(t *testing.T) *storage.BoltStorage {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

// FORGE_NATS_URL set → JetStream publisher + durable heartbeat store, and a
// subscriber on the coordinator's event channel really receives what the
// publisher sends (the wire contract for future consumers: CDC trigger,
// forge events CLI).
func TestSetupNATSWithNATSEnabled(t *testing.T) {
	t.Setenv(envNATSURL, startNATS(t))
	store := boltStore(t)

	publisher, hbStore, closeBus, err := setupNATS(store)
	require.NoError(t, err)
	defer closeBus()
	require.NotNil(t, publisher, "publisher must be built when NATS is configured")
	require.NotNil(t, hbStore, "heartbeat store must be built when NATS is configured")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nb, ok := publisher.(*bus.NATSBus)
	require.True(t, ok, "expected *bus.NATSBus, got %T", publisher)
	sub, err := nb.Subscribe(ctx, coordinator.EventChannel)
	require.NoError(t, err)

	require.NoError(t, publisher.Publish(ctx, coordinator.EventChannel, `{"type":"WORKFLOW_STARTED"}`))

	select {
	case payload := <-sub:
		assert.Contains(t, payload, "WORKFLOW_STARTED")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}

	// The heartbeat store round-trips a snapshot through NATS KV.
	require.NoError(t, hbStore.PutHeartbeat(ctx, coordinator.HeartbeatSnapshot{
		WorkerID: "w-kv", Addr: "127.0.0.1:1", Capacity: 4, Timestamp: time.Now(),
	}))
	require.NoError(t, hbStore.DeleteHeartbeat(ctx, "w-kv"))
}

// No NATS and Bolt storage → no publisher, no heartbeat store: events stay
// storage-only, exactly the pre-wiring behaviour.
func TestSetupNATSWithoutConfigIsSilentNoOp(t *testing.T) {
	t.Setenv(envNATSURL, "")

	publisher, hbStore, closeBus, err := setupNATS(boltStore(t))
	require.NoError(t, err)
	defer closeBus()
	assert.Nil(t, publisher)
	assert.Nil(t, hbStore)
}

// PostgreSQL storage without NATS falls back to the zero-dependency
// LISTEN/NOTIFY bus (PG mode only; CI runs it against the real service).
func TestSetupNATSFallsBackToPGNotify(t *testing.T) {
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	t.Setenv(envNATSURL, "")

	store, err := storage.NewPGStorage(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	publisher, hbStore, closeBus, err := setupNATS(store)
	require.NoError(t, err)
	defer closeBus()
	require.NotNil(t, publisher, "PG storage must yield the LISTEN/NOTIFY bus")
	assert.Nil(t, hbStore, "the KV heartbeat store needs NATS")
	_, ok := publisher.(*bus.PGNotifyBus)
	assert.True(t, ok, "expected *bus.PGNotifyBus, got %T", publisher)
}
