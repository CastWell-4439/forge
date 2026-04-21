package cache

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startEmbeddedNATS(t *testing.T) (*natsserver.Server, jetstream.JetStream) {
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
	require.True(t, srv.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	t.Cleanup(func() {
		nc.Close()
		srv.Shutdown()
	})

	return srv, js
}

func TestNATSKVHeartbeat_PutAndGet(t *testing.T) {
	_, js := startEmbeddedNATS(t)

	hb, err := NewNATSKVHeartbeat(js, DefaultNATSKVConfig())
	require.NoError(t, err)

	ctx := context.Background()

	err = hb.Put(ctx, HeartbeatInfo{
		WorkerID:    "w-001",
		Addr:        "localhost:50052",
		Capacity:    4,
		ActiveTasks: 1,
		Handlers:    []string{"ai.tts", "ai.face_swap"},
	})
	require.NoError(t, err)

	info, err := hb.Get(ctx, "w-001")
	require.NoError(t, err)
	assert.Equal(t, "w-001", info.WorkerID)
	assert.Equal(t, "localhost:50052", info.Addr)
	assert.Equal(t, 4, info.Capacity)
	assert.Equal(t, 1, info.ActiveTasks)
	assert.Len(t, info.Handlers, 2)
	assert.False(t, info.Timestamp.IsZero())
}

func TestNATSKVHeartbeat_Delete(t *testing.T) {
	_, js := startEmbeddedNATS(t)

	hb, err := NewNATSKVHeartbeat(js, DefaultNATSKVConfig())
	require.NoError(t, err)

	ctx := context.Background()
	err = hb.Put(ctx, HeartbeatInfo{WorkerID: "w-001", Addr: "host1"})
	require.NoError(t, err)

	err = hb.Delete(ctx, "w-001")
	require.NoError(t, err)

	_, err = hb.Get(ctx, "w-001")
	assert.Error(t, err)
}

func TestNATSKVHeartbeat_Keys(t *testing.T) {
	_, js := startEmbeddedNATS(t)

	hb, err := NewNATSKVHeartbeat(js, DefaultNATSKVConfig())
	require.NoError(t, err)

	ctx := context.Background()
	_ = hb.Put(ctx, HeartbeatInfo{WorkerID: "w-001", Addr: "host1"})
	_ = hb.Put(ctx, HeartbeatInfo{WorkerID: "w-002", Addr: "host2"})

	keys, err := hb.Keys(ctx)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "w-001")
	assert.Contains(t, keys, "w-002")
}

func TestNATSKVHeartbeat_Watch(t *testing.T) {
	_, js := startEmbeddedNATS(t)

	hb, err := NewNATSKVHeartbeat(js, DefaultNATSKVConfig())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	watchCh, err := hb.Watch(ctx)
	require.NoError(t, err)

	// Put a heartbeat — should trigger a watch event.
	time.Sleep(200 * time.Millisecond) // give watcher time to start
	_ = hb.Put(ctx, HeartbeatInfo{WorkerID: "w-watch", Addr: "host1"})

	select {
	case evt := <-watchCh:
		assert.Equal(t, HeartbeatPut, evt.Type)
		assert.Equal(t, "w-watch", evt.WorkerID)
		require.NotNil(t, evt.Info)
		assert.Equal(t, "host1", evt.Info.Addr)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}
