package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The client-only constructor is what makes etcd wiring possible outside the
// coordinator: it joins an existing etcd (embedded or cluster) without
// starting one, and every Discovery method works over it.
func TestNewEtcdClientAgainstEmbeddedServer(t *testing.T) {
	server := newTestEtcd(t)
	endpoints := []string{server.config.ClientAddr}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := NewEtcdClient(endpoints, "worker-1")
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Register + watch over the client.
	watchCh, err := client.Watch(ctx, "forge/workers/")
	require.NoError(t, err)
	require.NoError(t, client.Register(ctx, NodeInfo{
		ID:       "forge/workers/w-1",
		Addr:     "127.0.0.1:9090",
		Metadata: map[string]string{"handlers": "shell", "capacity": "4"},
	}))

	select {
	case evt := <-watchCh:
		require.Equal(t, EventAdd, evt.Type)
		assert.Equal(t, "forge/workers/w-1", evt.Node.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the watch event")
	}

	// Leader election over the client: a lone campaigner becomes leader.
	leaderCh, err := client.LeaderElect(ctx)
	require.NoError(t, err)
	select {
	case isLeader := <-leaderCh:
		assert.True(t, isLeader, "the only campaigner must win leadership")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for leadership")
	}
}

// Validation: no endpoints is a configuration error, and a malformed
// endpoint is refused at construction rather than at first use.
func TestNewEtcdClientValidatesInput(t *testing.T) {
	_, err := NewEtcdClient(nil, "x")
	assert.Error(t, err, "empty endpoint list must be refused")

	_, err = NewEtcdClient([]string{"://bad"}, "x")
	assert.Error(t, err, "malformed endpoint must be refused")
}
