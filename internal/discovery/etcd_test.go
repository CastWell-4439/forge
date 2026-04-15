package discovery

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getFreePort finds an available TCP port.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// newTestEtcd creates and starts an EtcdDiscovery for testing.
func newTestEtcd(t *testing.T) *EtcdDiscovery {
	t.Helper()

	clientPort := getFreePort(t)
	peerPort := getFreePort(t)

	d := NewEtcdDiscovery(EtcdConfig{
		Name:       fmt.Sprintf("test-%d", clientPort),
		ClientAddr: fmt.Sprintf("http://127.0.0.1:%d", clientPort),
		PeerAddr:   fmt.Sprintf("http://127.0.0.1:%d", peerPort),
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() { d.Close() })
	return d
}

func TestEtcdDiscovery_RegisterAndWatch(t *testing.T) {
	d := newTestEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start watching before registering so we catch the event.
	watchCh, err := d.Watch(ctx, "forge/nodes/")
	require.NoError(t, err)

	// Register a node.
	node := NodeInfo{
		ID:       "forge/nodes/worker-1",
		Addr:     "10.0.0.1:9090",
		Labels:   map[string]string{"gpu": "true"},
		Metadata: map[string]string{"version": "1.0"},
	}
	require.NoError(t, d.Register(ctx, node))

	// We should receive an EventAdd.
	select {
	case evt := <-watchCh:
		assert.Equal(t, EventAdd, evt.Type)
		assert.Equal(t, "forge/nodes/worker-1", evt.Node.ID)
		assert.Equal(t, "10.0.0.1:9090", evt.Node.Addr)
		assert.Equal(t, "true", evt.Node.Labels["gpu"])
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestEtcdDiscovery_Lock(t *testing.T) {
	d := newTestEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	unlock, err := d.Lock(ctx, "test-lock")
	require.NoError(t, err)

	// Try to acquire the same lock with a short timeout — should fail.
	lockCtx, lockCancel := context.WithTimeout(ctx, 1*time.Second)
	defer lockCancel()
	_, err = d.Lock(lockCtx, "test-lock")
	assert.Error(t, err, "second lock should fail due to timeout")

	// Release the first lock.
	unlock()

	// Now acquiring should succeed.
	unlock2, err := d.Lock(ctx, "test-lock")
	require.NoError(t, err)
	unlock2()
}

func TestEtcdDiscovery_LeaderElect(t *testing.T) {
	d := newTestEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ch, err := d.LeaderElect(ctx)
	require.NoError(t, err)

	// With a single node, it should become leader quickly.
	select {
	case isLeader := <-ch:
		assert.True(t, isLeader, "single node should become leader")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for leader election")
	}
}

func TestEtcdDiscovery_WatchDelete(t *testing.T) {
	d := newTestEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Register a node with a short-lived context so it will expire.
	regCtx, regCancel := context.WithCancel(ctx)
	node := NodeInfo{
		ID:   "forge/nodes/worker-ephemeral",
		Addr: "10.0.0.2:9090",
	}
	require.NoError(t, d.Register(regCtx, node))

	// Watch for events.
	watchCh, err := d.Watch(ctx, "forge/nodes/")
	require.NoError(t, err)

	// Consume the initial EventAdd from the listing.
	select {
	case evt := <-watchCh:
		assert.Equal(t, EventAdd, evt.Type)
		assert.Equal(t, "forge/nodes/worker-ephemeral", evt.Node.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for initial add event")
	}

	// Cancel the registration context to trigger session expiry.
	regCancel()

	// We should eventually receive an EventDelete when the lease expires.
	select {
	case evt := <-watchCh:
		assert.Equal(t, EventDelete, evt.Type)
		assert.Equal(t, "forge/nodes/worker-ephemeral", evt.Node.ID)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for delete event after session expiry")
	}
}
