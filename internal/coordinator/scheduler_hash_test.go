package coordinator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsistentHashScheduler_SameHandlerSameWorker(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 100, Weight: 1},
		{ID: "w2", ActiveTasks: 0, Capacity: 100, Weight: 1},
		{ID: "w3", ActiveTasks: 0, Capacity: 100, Weight: 1},
	}

	task := &SchedulerTask{ID: "t1", Handler: "video.render"}

	// Same handler should consistently pick the same worker
	first, err := sched.Schedule(task, workers)
	require.NoError(t, err)

	for i := 0; i < 20; i++ {
		w, err := sched.Schedule(&SchedulerTask{ID: "t-other", Handler: "video.render"}, workers)
		require.NoError(t, err)
		assert.Equal(t, first.ID, w.ID, "same handler should route to same worker")
	}
}

func TestConsistentHashScheduler_DifferentHandlersMayDiffer(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 100, Weight: 1},
		{ID: "w2", ActiveTasks: 0, Capacity: 100, Weight: 1},
		{ID: "w3", ActiveTasks: 0, Capacity: 100, Weight: 1},
	}

	// Schedule many different handlers and verify we get distribution
	assigned := map[string]bool{}
	handlers := []string{"video.render", "ai.generate", "data.process", "email.send", "report.build"}
	for _, h := range handlers {
		w, err := sched.Schedule(&SchedulerTask{ID: "t1", Handler: h}, workers)
		require.NoError(t, err)
		assigned[w.ID] = true
	}
	// With 5 handlers and 3 workers, we should see at least 2 different workers
	assert.GreaterOrEqual(t, len(assigned), 2, "different handlers should distribute across workers")
}

func TestConsistentHashScheduler_FallbackOnCapacity(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 100, Weight: 1},
		{ID: "w2", ActiveTasks: 0, Capacity: 100, Weight: 1},
	}

	task := &SchedulerTask{ID: "t1", Handler: "video.render"}

	// Find which worker is chosen
	primary, err := sched.Schedule(task, workers)
	require.NoError(t, err)

	// Now mark that worker as at capacity
	for _, w := range workers {
		if w.ID == primary.ID {
			w.ActiveTasks = w.Capacity
		}
	}

	// Should fall back to the other worker
	fallback, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.NotEqual(t, primary.ID, fallback.ID, "should fallback to another worker when primary is at capacity")
}

func TestConsistentHashScheduler_AllAtCapacity(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 10, Capacity: 10, Weight: 1},
		{ID: "w2", ActiveTasks: 5, Capacity: 5, Weight: 1},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	_, err := sched.Schedule(task, workers)
	assert.ErrorIs(t, err, ErrNoWorkerAvailable)
}

func TestConsistentHashScheduler_RespectsLabels(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 10, Weight: 1, Labels: map[string]string{"gpu": "true"}},
		{ID: "w2", ActiveTasks: 0, Capacity: 10, Weight: 1, Labels: map[string]string{"gpu": "false"}},
	}
	task := &SchedulerTask{ID: "t1", Handler: "video.render", MatchLabels: map[string]string{"gpu": "true"}}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w1", w.ID)
}

func TestConsistentHashScheduler_SingleWorker(t *testing.T) {
	sched := NewConsistentHashScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 100, Weight: 1},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w1", w.ID)
}
