package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/castwell/forge/internal/discovery"
	"github.com/castwell/forge/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerManager_AddAndGetWorker(t *testing.T) {
	d := newTestEtcd(t)
	wm := NewWorkerManager(d)

	// Register a worker via etcd.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, wm.WatchWorkers(ctx))

	// Register the worker node in etcd.
	node := discovery.NodeInfo{
		ID:       "forge/workers/worker-1",
		Addr:     "127.0.0.1:19090",
		Labels:   map[string]string{"gpu": "true"},
		Metadata: map[string]string{"handlers": "handler.a,handler.b", "capacity": "5"},
	}
	require.NoError(t, d.Register(ctx, node))

	// Wait for the worker to appear.
	require.Eventually(t, func() bool {
		return wm.GetWorker("forge/workers/worker-1") != nil
	}, 10*time.Second, 100*time.Millisecond)

	w := wm.GetWorker("forge/workers/worker-1")
	assert.Equal(t, WorkerStatusActive, w.Status)
	assert.Equal(t, 5, w.Capacity)
	assert.Contains(t, w.Handlers, "handler.a")
	assert.Contains(t, w.Handlers, "handler.b")
}

func TestWorkerManager_ActiveWorkers(t *testing.T) {
	wm := NewWorkerManager(nil) // no discovery needed for direct add

	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))
	require.NoError(t, wm.AddWorkerDirect("w2", "127.0.0.1:19092", []string{"h2"}, 3))

	active := wm.ActiveWorkers()
	assert.Len(t, active, 2)
}

func TestWorkerManager_UpdateHeartbeat(t *testing.T) {
	wm := NewWorkerManager(nil)
	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))

	// Simulate heartbeat.
	wm.UpdateHeartbeat("w1", 3, 10)

	w := wm.GetWorker("w1")
	assert.Equal(t, WorkerStatusActive, w.Status)
	assert.Equal(t, 3, w.ActiveTasks)
	assert.Equal(t, 10, w.Capacity)
}

func TestWorkerManager_FailureDetection_Suspect(t *testing.T) {
	wm := NewWorkerManager(nil)
	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))

	// Backdate the last heartbeat to trigger SUSPECT.
	wm.mu.Lock()
	wm.workers["w1"].LastHeartbeat = time.Now().Add(-35 * time.Second)
	wm.mu.Unlock()

	wm.checkWorkerHealth()

	w := wm.GetWorker("w1")
	assert.Equal(t, WorkerStatusSuspect, w.Status)
}

func TestWorkerManager_FailureDetection_Dead(t *testing.T) {
	wm := NewWorkerManager(nil)
	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))

	deadCalled := make(chan string, 1)
	wm.OnWorkerDead(func(id string) {
		deadCalled <- id
	})

	// Backdate the last heartbeat to trigger DEAD.
	wm.mu.Lock()
	wm.workers["w1"].LastHeartbeat = time.Now().Add(-65 * time.Second)
	wm.mu.Unlock()

	wm.checkWorkerHealth()

	select {
	case id := <-deadCalled:
		assert.Equal(t, "w1", id)
	case <-time.After(1 * time.Second):
		t.Fatal("onWorkerDead callback not called")
	}

	w := wm.GetWorker("w1")
	assert.Equal(t, WorkerStatusDead, w.Status)
}

func TestWorkerManager_Recovery(t *testing.T) {
	wm := NewWorkerManager(nil)
	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))

	// Make worker SUSPECT.
	wm.mu.Lock()
	wm.workers["w1"].LastHeartbeat = time.Now().Add(-35 * time.Second)
	wm.mu.Unlock()
	wm.checkWorkerHealth()
	assert.Equal(t, WorkerStatusSuspect, wm.GetWorker("w1").Status)

	// Heartbeat received -> should recover to ACTIVE.
	wm.UpdateHeartbeat("w1", 1, 5)
	assert.Equal(t, WorkerStatusActive, wm.GetWorker("w1").Status)
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		key      string
		want     []string
	}{
		{
			name:     "comma separated",
			metadata: map[string]string{"handlers": "a, b, c"},
			key:      "handlers",
			want:     []string{"a", "b", "c"},
		},
		{
			name:     "nil metadata",
			metadata: nil,
			key:      "handlers",
			want:     nil,
		},
		{
			name:     "empty value",
			metadata: map[string]string{"handlers": ""},
			key:      "handlers",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLabels(tt.metadata, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseIntLabel(t *testing.T) {
	assert.Equal(t, 5, parseIntLabel(map[string]string{"capacity": "5"}, "capacity", 10))
	assert.Equal(t, 10, parseIntLabel(nil, "capacity", 10))
	assert.Equal(t, 10, parseIntLabel(map[string]string{}, "capacity", 10))
	assert.Equal(t, 10, parseIntLabel(map[string]string{"capacity": "abc"}, "capacity", 10))
}

func TestCoordinator_RescheduleDeadWorkerTasks(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	c := NewCoordinator(store)
	wm := NewWorkerManager(nil)
	c.SetWorkerManager(wm)

	ctx := context.Background()

	// Create a workflow with a task assigned to worker-1.
	wf := &storage.Workflow{
		ID:     "wf-1",
		Name:   "test-wf",
		Status: storage.WorkflowStatusRunning,
	}
	require.NoError(t, store.SaveWorkflow(ctx, wf))

	task := &storage.Task{
		ID:         "task-1",
		WorkflowID: "wf-1",
		TaskName:   "do-something",
		Handler:    "handler.a",
		Status:     storage.TaskStatusRunning,
		WorkerID:   "worker-1",
	}
	require.NoError(t, store.SaveTask(ctx, task))

	// Simulate dead worker callback.
	c.rescheduleDeadWorkerTasks("worker-1")

	// Give async scheduling a moment.
	time.Sleep(100 * time.Millisecond)

	// Task should be reset to READY.
	updatedTask, err := store.GetTask(ctx, "task-1")
	require.NoError(t, err)
	assert.Equal(t, storage.TaskStatusReady, updatedTask.Status)
}

func TestWorkerManager_FailureDetector_RunLoop(t *testing.T) {
	wm := NewWorkerManager(nil)
	require.NoError(t, wm.AddWorkerDirect("w1", "127.0.0.1:19091", []string{"h1"}, 5))

	// Backdate heartbeat so it will be marked DEAD.
	wm.mu.Lock()
	wm.workers["w1"].LastHeartbeat = time.Now().Add(-65 * time.Second)
	wm.mu.Unlock()

	deadCalled := make(chan string, 1)
	wm.OnWorkerDead(func(id string) {
		deadCalled <- id
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go wm.RunFailureDetector(ctx)

	select {
	case id := <-deadCalled:
		assert.Equal(t, "w1", id)
	case <-time.After(10 * time.Second):
		t.Fatal("RunFailureDetector did not detect dead worker")
	}
}
