package worker

import (
	"testing"
	"time"
	"unique"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerManager_RegisterAndGet(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-001", "localhost:50052", 4, []string{"ai.tts", "ai.face_swap"}, nil)

	info, ok := mgr.Get("w-001")
	require.True(t, ok)
	assert.Equal(t, "localhost:50052", info.Addr)
	assert.Equal(t, 4, info.Capacity)
	assert.Len(t, info.Handlers, 2)
	assert.Equal(t, "w-001", WorkerID(info.ID))
}

func TestWorkerManager_UniqueHandleDedup(t *testing.T) {
	mgr := NewWorkerManager()
	// Two workers with the same handler name should share the same unique.Handle.
	mgr.Register("w-001", "host1:50052", 2, []string{"ai.tts"}, nil)
	mgr.Register("w-002", "host2:50052", 2, []string{"ai.tts"}, nil)

	info1, _ := mgr.Get("w-001")
	info2, _ := mgr.Get("w-002")

	// unique.Make for the same string returns equal handles.
	assert.Equal(t, info1.Handlers[0], info2.Handlers[0])
	assert.Equal(t, unique.Make("ai.tts"), info1.Handlers[0])
}

func TestWorkerManager_Heartbeat(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-001", "localhost:50052", 4, nil, nil)

	assert.True(t, mgr.Heartbeat("w-001"))
	assert.False(t, mgr.Heartbeat("w-nonexistent"))
}

func TestWorkerManager_Deregister(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-001", "localhost:50052", 4, nil, nil)
	assert.Equal(t, 1, mgr.Count())

	mgr.Deregister("w-001")
	assert.Equal(t, 0, mgr.Count())

	_, ok := mgr.Get("w-001")
	assert.False(t, ok)
}

func TestWorkerManager_FindByHandler(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-001", "host1", 2, []string{"ai.tts", "ai.face_swap"}, nil)
	mgr.Register("w-002", "host2", 4, []string{"ai.tts"}, nil)
	mgr.Register("w-003", "host3", 8, []string{"video.encode"}, nil)

	ttsWorkers := mgr.FindByHandler("ai.tts")
	assert.Len(t, ttsWorkers, 2)

	videoWorkers := mgr.FindByHandler("video.encode")
	assert.Len(t, videoWorkers, 1)

	noWorkers := mgr.FindByHandler("nonexistent")
	assert.Empty(t, noWorkers)
}

func TestWorkerManager_ActiveWorkers(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-001", "host1", 2, nil, nil)
	mgr.Register("w-002", "host2", 4, nil, nil)

	active := mgr.ActiveWorkers(1 * time.Minute)
	assert.Len(t, active, 2)

	// Nothing is older than 0.
	old := mgr.ActiveWorkers(0)
	assert.Empty(t, old)
}

func TestWorkerManager_Labels(t *testing.T) {
	mgr := NewWorkerManager()
	mgr.Register("w-gpu-1", "host1", 1, []string{"ai.tts"}, map[string]string{
		"gpu": "A100",
		"zone": "us-east-1",
	})

	info, ok := mgr.Get("w-gpu-1")
	require.True(t, ok)
	assert.Equal(t, "A100", info.Labels["gpu"])
}
