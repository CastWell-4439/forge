package bus

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startEmbeddedNATS starts an in-process NATS server with JetStream enabled.
func startEmbeddedNATS(t *testing.T) (*natsserver.Server, *nats.Conn) {
	t.Helper()

	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random port
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}

	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)

	srv.Start()
	require.True(t, srv.ReadyForConnections(5*time.Second), "NATS server not ready")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)

	t.Cleanup(func() {
		nc.Close()
		srv.Shutdown()
	})

	return srv, nc
}

func TestNATSBus_PublishAndSubscribe(t *testing.T) {
	_, nc := startEmbeddedNATS(t)

	bus, err := NewNATSBus(nc, DefaultNATSConfig())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe to "test-handler" channel.
	ch, err := bus.Subscribe(ctx, "test-handler")
	require.NoError(t, err)

	// Give consumer time to be ready.
	time.Sleep(500 * time.Millisecond)

	// Publish a message.
	err = bus.Publish(ctx, "test-handler", `{"task":"hello"}`)
	require.NoError(t, err)

	// Receive.
	select {
	case msg := <-ch:
		assert.Contains(t, msg, "hello")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestNATSBus_PublishTask_Dedup(t *testing.T) {
	_, nc := startEmbeddedNATS(t)

	bus, err := NewNATSBus(nc, DefaultNATSConfig())
	require.NoError(t, err)

	ctx := context.Background()

	// Publish the same task ID twice — JetStream should dedup.
	err = bus.PublishTask(ctx, "task-001", "ai.tts", map[string]string{"text": "hello"})
	require.NoError(t, err)

	err = bus.PublishTask(ctx, "task-001", "ai.tts", map[string]string{"text": "hello"})
	require.NoError(t, err)

	// Both publishes succeed (dedup is silent), but consumer only sees one message.
	// We just verify no error on duplicate.
}

func TestNATSBus_PublishEvent(t *testing.T) {
	_, nc := startEmbeddedNATS(t)

	bus, err := NewNATSBus(nc, DefaultNATSConfig())
	require.NoError(t, err)

	ctx := context.Background()
	err = bus.PublishEvent(ctx, "wf-123", map[string]string{"type": "task_completed"})
	require.NoError(t, err)
}

func TestNATSBus_Close(t *testing.T) {
	_, nc := startEmbeddedNATS(t)

	bus, err := NewNATSBus(nc, DefaultNATSConfig())
	require.NoError(t, err)
	require.NoError(t, bus.Close())
}
