package coordinator

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/castwell/forge/internal/discovery"
	"github.com/castwell/forge/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func newTestEtcd(t *testing.T) *discovery.EtcdDiscovery {
	t.Helper()
	clientPort := getFreePort(t)
	peerPort := getFreePort(t)
	d := discovery.NewEtcdDiscovery(discovery.EtcdConfig{
		Name:       fmt.Sprintf("test-%d", clientPort),
		ClientAddr: fmt.Sprintf("http://127.0.0.1:%d", clientPort),
		PeerAddr:   fmt.Sprintf("http://127.0.0.1:%d", peerPort),
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() { d.Close() })
	return d
}

func TestLeaderController_SingleNode(t *testing.T) {
	d := newTestEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	lc := NewLeaderController(d, "node-1")

	gained := make(chan struct{}, 1)
	lc.OnGain(func() { gained <- struct{}{} })

	go lc.Run(ctx)

	select {
	case <-gained:
		assert.True(t, lc.IsLeader())
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for leadership")
	}
}

func TestCoordinator_IsLeader_Standalone(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	c := NewCoordinator(store)
	// Without discovery configured, standalone mode: always leader.
	assert.True(t, c.IsLeader())
}

func TestCoordinator_SetDiscovery(t *testing.T) {
	d := newTestEtcd(t)
	dbPath := t.TempDir() + "/test.db"
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	c := NewCoordinator(store)
	c.SetDiscovery(d, "coord-1")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Before election, not leader.
	assert.False(t, c.IsLeader())

	require.NoError(t, c.StartLeaderElection(ctx))

	// Wait for leadership.
	require.Eventually(t, func() bool {
		return c.IsLeader()
	}, 10*time.Second, 100*time.Millisecond, "coordinator should become leader")
}
