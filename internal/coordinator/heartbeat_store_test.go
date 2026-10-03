package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHeartbeatStore records the durable heartbeat lifecycle.
type fakeHeartbeatStore struct {
	mu      sync.Mutex
	puts    []HeartbeatSnapshot
	deletes []string
}

func (f *fakeHeartbeatStore) PutHeartbeat(_ context.Context, snap HeartbeatSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, snap)
	return nil
}

func (f *fakeHeartbeatStore) DeleteHeartbeat(_ context.Context, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, workerID)
	return nil
}

// The durable copy follows the worker's lifecycle: register → put,
// heartbeat → refreshed put, DEAD → delete, explicit removal → delete.
// This is README's "NATS (消息+心跳)" with the gRPC stream untouched.
func TestHeartbeatStoreLifecycle(t *testing.T) {
	wm := NewWorkerManager(nil)
	store := &fakeHeartbeatStore{}
	wm.SetHeartbeatStore(store)

	// Register (direct mode — no etcd needed for the store hooks).
	require.NoError(t, wm.AddWorkerDirect("w-1", "127.0.0.1:1", []string{"shell"}, 5))
	require.Len(t, store.puts, 1, "registration persists a snapshot")
	assert.Equal(t, "w-1", store.puts[0].WorkerID)
	assert.Equal(t, "127.0.0.1:1", store.puts[0].Addr)
	assert.Equal(t, []string{"shell"}, store.puts[0].Handlers)

	// A heartbeat refreshes the durable copy with live counts.
	wm.UpdateHeartbeat("w-1", 2, 5)
	require.Len(t, store.puts, 2, "each heartbeat refreshes the snapshot")
	assert.Equal(t, 2, store.puts[1].ActiveTasks)
	assert.Equal(t, 5, store.puts[1].Capacity)

	// Backdate the last heartbeat past the dead threshold: the failure
	// detector marks the worker DEAD and the durable copy is removed.
	wm.mu.Lock()
	wm.workers["w-1"].LastHeartbeat = time.Now().Add(-deadThreshold - time.Second)
	wm.mu.Unlock()
	wm.checkWorkerHealth()
	assert.Equal(t, []string{"w-1"}, store.deletes, "DEAD removes the snapshot")

	// Explicit removal deletes too.
	require.NoError(t, wm.AddWorkerDirect("w-2", "127.0.0.1:2", []string{"git"}, 3))
	wm.removeWorker("w-2")
	assert.Contains(t, store.deletes, "w-2", "removal deletes the snapshot")
}

// No store installed = heartbeats stay memory-only: every hook is a no-op
// and nothing panics (the pre-wiring behaviour).
func TestHeartbeatStoreNilStaysMemoryOnly(t *testing.T) {
	wm := NewWorkerManager(nil)

	require.NoError(t, wm.AddWorkerDirect("w-3", "127.0.0.1:3", []string{"shell"}, 1))
	wm.UpdateHeartbeat("w-3", 0, 1)
	wm.checkWorkerHealth()
	wm.removeWorker("w-3")
}

// A store outage degrades persistence, never liveness: put/delete failures
// are logged and swallowed, the in-memory state keeps working.
func TestHeartbeatStoreFailureDoesNotBreakTracking(t *testing.T) {
	wm := NewWorkerManager(nil)
	wm.SetHeartbeatStore(failingHeartbeatStore{})

	require.NoError(t, wm.AddWorkerDirect("w-4", "127.0.0.1:4", []string{"shell"}, 1))
	wm.UpdateHeartbeat("w-4", 1, 1)
	// The worker is still tracked and heartbeats still land in memory.
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	require.Contains(t, wm.workers, "w-4")
	assert.Equal(t, 1, wm.workers["w-4"].ActiveTasks)
}

type failingHeartbeatStore struct{}

func (failingHeartbeatStore) PutHeartbeat(context.Context, HeartbeatSnapshot) error {
	return assert.AnError
}

func (failingHeartbeatStore) DeleteHeartbeat(context.Context, string) error {
	return assert.AnError
}
