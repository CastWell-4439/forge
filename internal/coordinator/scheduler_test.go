package coordinator

import (
	"testing"

	"github.com/castwell/forge/internal/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterByLabels_NoMatchLabels(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Labels: map[string]string{"gpu": "true"}},
		{ID: "w2", Labels: map[string]string{"region": "us-east"}},
	}
	result, err := FilterByLabels(workers, nil)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestFilterByLabels_EmptyMatchLabels(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Labels: map[string]string{"gpu": "true"}},
	}
	result, err := FilterByLabels(workers, map[string]string{})
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

func TestFilterByLabels_SingleLabel(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Labels: map[string]string{"gpu": "true", "region": "cn-north"}},
		{ID: "w2", Labels: map[string]string{"region": "cn-north"}},
		{ID: "w3", Labels: map[string]string{"gpu": "true", "region": "us-east"}},
	}
	result, err := FilterByLabels(workers, map[string]string{"gpu": "true"})
	require.NoError(t, err)
	assert.Len(t, result, 2)
	ids := []string{result[0].ID, result[1].ID}
	assert.Contains(t, ids, "w1")
	assert.Contains(t, ids, "w3")
}

func TestFilterByLabels_MultipleLabels(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Labels: map[string]string{"gpu": "true", "region": "cn-north"}},
		{ID: "w2", Labels: map[string]string{"gpu": "true", "region": "us-east"}},
		{ID: "w3", Labels: map[string]string{"region": "cn-north"}},
	}
	result, err := FilterByLabels(workers, map[string]string{"gpu": "true", "region": "cn-north"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "w1", result[0].ID)
}

func TestFilterByLabels_NoMatch(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", Labels: map[string]string{"region": "us-east"}},
		{ID: "w2", Labels: map[string]string{"region": "eu-west"}},
	}
	result, err := FilterByLabels(workers, map[string]string{"gpu": "true"})
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrNoMatchingLabels)
}

func TestFilterAtCapacity(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 5, Capacity: 5},
		{ID: "w2", ActiveTasks: 3, Capacity: 5},
		{ID: "w3", ActiveTasks: 10, Capacity: 10},
	}
	result := filterAtCapacity(workers)
	require.Len(t, result, 1)
	assert.Equal(t, "w2", result[0].ID)
}

func TestFilterAtCapacity_AllAvailable(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 0, Capacity: 10},
		{ID: "w2", ActiveTasks: 5, Capacity: 10},
	}
	result := filterAtCapacity(workers)
	assert.Len(t, result, 2)
}

func TestFilterAtCapacity_NoneAvailable(t *testing.T) {
	workers := []*SchedulerWorkerInfo{
		{ID: "w1", ActiveTasks: 10, Capacity: 10},
	}
	result := filterAtCapacity(workers)
	assert.Empty(t, result)
}

func TestToSchedulerWorkerInfo(t *testing.T) {
	wi := &WorkerInfo{
		Registration: discovery.NodeInfo{
			ID:   "worker-1",
			Addr: "localhost:9090",
			Labels: map[string]string{
				"gpu":    "true",
				"region": "cn-north",
			},
			Metadata: map[string]string{
				"handlers": "ai.generate,video.render",
				"capacity": "20",
				"weight":   "5",
			},
		},
		Handlers:    []string{"ai.generate", "video.render"},
		Capacity:    20,
		ActiveTasks: 3,
	}

	swi := ToSchedulerWorkerInfo(wi)
	assert.Equal(t, "worker-1", swi.ID)
	assert.Equal(t, "localhost:9090", swi.Addr)
	assert.Equal(t, 3, swi.ActiveTasks)
	assert.Equal(t, 20, swi.Capacity)
	assert.Equal(t, 5, swi.Weight)
	assert.Equal(t, "true", swi.Labels["gpu"])
	assert.Equal(t, "cn-north", swi.Labels["region"])
	// Internal keys should be excluded
	assert.NotContains(t, swi.Labels, "handlers")
	assert.NotContains(t, swi.Labels, "capacity")
}

func TestToSchedulerWorkerInfo_DefaultWeight(t *testing.T) {
	wi := &WorkerInfo{
		Registration: discovery.NodeInfo{
			ID:   "worker-2",
			Addr: "localhost:9091",
		},
		Capacity: 10,
	}

	swi := ToSchedulerWorkerInfo(wi)
	assert.Equal(t, 1, swi.Weight) // default weight
}
