package main

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/discovery"
	"github.com/castwell/forge/internal/storage"
)

func newTestCoordinator(t *testing.T) *coordinator.Coordinator {
	t.Helper()
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return coordinator.NewCoordinator(store)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// Without FORGE_ETCD_* nothing is wired: no error, no goroutines, and the
// coordinator stays standalone (always leader) — the pre-wiring behaviour.
func TestSetupDiscoveryStandaloneIsANoOp(t *testing.T) {
	t.Setenv(envEtcdEmbed, "")
	t.Setenv(envEtcdEndpoints, "")

	coord := newTestCoordinator(t)
	stop, err := setupDiscovery(context.Background(), coord, nil)
	require.NoError(t, err)
	defer stop()

	assert.True(t, coord.IsLeader(), "standalone coordinator is always leader")
}

// The full distributed assembly over an embedded etcd: the coordinator
// elects itself leader, and a worker that registers in etcd becomes visible
// through ListWorkers — proving SetWorkerManager + WatchWorkers + the
// discovery path are actually connected.
func TestSetupDiscoveryEmbeddedElectsLeaderAndSeesDiscoveredWorkers(t *testing.T) {
	clientPort := freeTCPPort(t)
	peerPort := freeTCPPort(t)
	clientAddr := fmt.Sprintf("http://127.0.0.1:%d", clientPort)
	t.Setenv(envEtcdEmbed, "1")
	t.Setenv(envEtcdEndpoints, "") // embed takes precedence anyway; keep explicit
	t.Setenv(envEtcdClientAddr, clientAddr)
	t.Setenv(envEtcdPeerAddr, fmt.Sprintf("http://127.0.0.1:%d", peerPort))
	t.Setenv(envEtcdDataDir, t.TempDir())
	t.Setenv(envCoordID, "coord-test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord := newTestCoordinator(t)
	stop, err := setupDiscovery(ctx, coord, nil)
	require.NoError(t, err)
	defer stop()

	// Leadership: the single coordinator wins its election.
	require.Eventually(t, coord.IsLeader, 15*time.Second, 100*time.Millisecond,
		"coordinator should become leader")

	// A worker registers through the etcd client (as cmd/worker does).
	reg, err := discovery.NewEtcdClient([]string{clientAddr}, "worker-1")
	require.NoError(t, err)
	t.Cleanup(func() { reg.Close() })
	require.NoError(t, reg.Register(ctx, discovery.NodeInfo{
		ID:       "forge/workers/worker-1",
		Addr:     "127.0.0.1:1", // unreachable on purpose: discovery is about visibility
		Metadata: map[string]string{"handlers": "shell", "capacity": "4"},
	}))

	// The discovered worker surfaces through the coordinator's production API.
	require.Eventually(t, func() bool {
		resp, err := coord.ListWorkers(ctx, &forgev1.ListWorkersRequest{})
		return err == nil && len(resp.GetWorkers()) >= 1
	}, 10*time.Second, 100*time.Millisecond,
		"a worker registered in etcd must appear in ListWorkers")
}

// FORGE_ETCD_ENDPOINTS without embed connects to an existing server instead
// of starting one — the external-cluster path.
func TestSetupDiscoveryConnectsToExternalEndpoints(t *testing.T) {
	// A local embedded instance plays the "external" cluster.
	clientPort := freeTCPPort(t)
	peerPort := freeTCPPort(t)
	clientAddr := fmt.Sprintf("http://127.0.0.1:%d", clientPort)
	external := discovery.NewEtcdDiscovery(discovery.EtcdConfig{
		Name:       fmt.Sprintf("ext-%d", clientPort),
		ClientAddr: clientAddr,
		PeerAddr:   fmt.Sprintf("http://127.0.0.1:%d", peerPort),
	})
	require.NoError(t, external.Start())
	t.Cleanup(func() { external.Close() })

	t.Setenv(envEtcdEmbed, "")
	t.Setenv(envEtcdEndpoints, clientAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord := newTestCoordinator(t)
	stop, err := setupDiscovery(ctx, coord, nil)
	require.NoError(t, err)
	defer stop()

	require.Eventually(t, coord.IsLeader, 15*time.Second, 100*time.Millisecond,
		"coordinator should win leadership on the external etcd")
}
