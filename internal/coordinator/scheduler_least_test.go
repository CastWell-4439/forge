package coordinator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeastActiveScheduler_PicksLowest(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 5, Capacity: 10, Weight: 1},
		{ID: "w2", ActiveTasks: 2, Capacity: 10, Weight: 1},
		{ID: "w3", ActiveTasks: 8, Capacity: 10, Weight: 1},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w2", w.ID)
}

func TestLeastActiveScheduler_TieBreakByWeight(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 3, Capacity: 10, Weight: 1},
		{ID: "w2", ActiveTasks: 3, Capacity: 10, Weight: 5},
		{ID: "w3", ActiveTasks: 3, Capacity: 10, Weight: 3},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w2", w.ID, "should pick w2 with highest weight on tie")
}

func TestLeastActiveScheduler_SkipsAtCapacity(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 0},  // at capacity (0/0)
		{ID: "w2", ActiveTasks: 5, Capacity: 10, Weight: 1},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w2", w.ID)
}

func TestLeastActiveScheduler_AllAtCapacity(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 10, Capacity: 10},
		{ID: "w2", ActiveTasks: 5, Capacity: 5},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	_, err := sched.Schedule(task, workers)
	assert.ErrorIs(t, err, ErrNoWorkerAvailable)
}

func TestLeastActiveScheduler_RespectsLabels(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 1, Capacity: 10, Weight: 1, Labels: map[string]string{"gpu": "false"}},
		{ID: "w2", ActiveTasks: 5, Capacity: 10, Weight: 1, Labels: map[string]string{"gpu": "true"}},
		{ID: "w3", ActiveTasks: 3, Capacity: 10, Weight: 1, Labels: map[string]string{"gpu": "true"}},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test", MatchLabels: map[string]string{"gpu": "true"}}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w3", w.ID, "should pick w3 (lowest active among gpu workers)")
}

func TestLeastActiveScheduler_SingleWorker(t *testing.T) {
	sched := NewLeastActiveScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 10, Weight: 1},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w1", w.ID)
}
