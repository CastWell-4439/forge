package coordinator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWRRScheduler_BasicDistribution(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 2, ActiveTasks: 0, Capacity: 100},
		{ID: "w2", Weight: 1, ActiveTasks: 0, Capacity: 100},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	counts := map[string]int{}
	iterations := 300
	for i := 0; i < iterations; i++ {
		w, err := sched.Schedule(task, workers)
		require.NoError(t, err)
		counts[w.ID]++
	}

	// With weights 2:1, w1 should get roughly 2x the tasks of w2
	ratio := float64(counts["w1"]) / float64(counts["w2"])
	assert.InDelta(t, 2.0, ratio, 0.5, "WRR ratio should be close to 2:1, got %v:%v", counts["w1"], counts["w2"])
}

func TestWRRScheduler_EqualWeights(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 1, ActiveTasks: 0, Capacity: 100},
		{ID: "w2", Weight: 1, ActiveTasks: 0, Capacity: 100},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		w, err := sched.Schedule(task, workers)
		require.NoError(t, err)
		counts[w.ID]++
	}

	// Equal weights should give roughly equal distribution
	assert.InDelta(t, 50, counts["w1"], 10)
	assert.InDelta(t, 50, counts["w2"], 10)
}

func TestWRRScheduler_SkipsAtCapacity(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 1, ActiveTasks: 10, Capacity: 10}, // full
		{ID: "w2", Weight: 1, ActiveTasks: 0, Capacity: 10},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w2", w.ID)
}

func TestWRRScheduler_AllAtCapacity(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 1, ActiveTasks: 10, Capacity: 10},
		{ID: "w2", Weight: 1, ActiveTasks: 5, Capacity: 5},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	_, err := sched.Schedule(task, workers)
	assert.ErrorIs(t, err, ErrNoWorkerAvailable)
}

func TestWRRScheduler_RespectsLabels(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 1, ActiveTasks: 0, Capacity: 10, Labels: map[string]string{"gpu": "true"}},
		{ID: "w2", Weight: 1, ActiveTasks: 0, Capacity: 10, Labels: map[string]string{"gpu": "false"}},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test", MatchLabels: map[string]string{"gpu": "true"}}

	w, err := sched.Schedule(task, workers)
	require.NoError(t, err)
	assert.Equal(t, "w1", w.ID)
}

func TestWRRScheduler_NoLabelMatch(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 1, ActiveTasks: 0, Capacity: 10, Labels: map[string]string{"region": "us"}},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test", MatchLabels: map[string]string{"gpu": "true"}}

	_, err := sched.Schedule(task, workers)
	assert.ErrorIs(t, err, ErrNoMatchingLabels)
}

func TestWRRScheduler_SingleWorker(t *testing.T) {
	sched := NewWRRScheduler()
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Weight: 3, ActiveTasks: 0, Capacity: 100},
	}
	task := &SchedulerTask{ID: "t1", Handler: "test"}

	for i := 0; i < 10; i++ {
		w, err := sched.Schedule(task, workers)
		require.NoError(t, err)
		assert.Equal(t, "w1", w.ID)
	}
}

func TestGCD(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{6, 4, 2},
		{5, 5, 5},
		{7, 3, 1},
		{0, 5, 5},
		{12, 8, 4},
	}
	for _, tc := range tests {
		got := gcd(tc.a, tc.b)
		assert.Equal(t, tc.want, got, "gcd(%d, %d)", tc.a, tc.b)
	}
}

func TestGCD_Zero(t *testing.T) {
	// gcd(0,0) should return 1 to avoid division by zero
	assert.Equal(t, 1, gcd(0, 0))
}
