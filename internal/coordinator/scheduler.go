// Package coordinator implements the core coordination logic for Forge,
// including DAG workflow parsing, validation, and execution orchestration.
package coordinator

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoWorkerAvailable is returned when no worker can handle the task.
var ErrNoWorkerAvailable = errors.New("no worker available")

// ErrNoMatchingLabels is returned when no worker matches the task's label selector.
var ErrNoMatchingLabels = errors.New("no worker matches task label selector")

// Scheduler selects the best worker for a given task.
type Scheduler interface {
	// Schedule picks the most suitable worker for the task.
	Schedule(task *SchedulerTask, workers []*SchedulerWorkerInfo) (*SchedulerWorkerInfo, error)
}

// SchedulerWorkerInfo is a lightweight worker descriptor used by scheduling
// algorithms. It decouples the scheduler from gRPC connection details held
// by the full WorkerInfo struct in worker_manager.go.
type SchedulerWorkerInfo struct {
	ID            string
	Addr          string
	Labels        map[string]string
	ActiveTasks   int
	Capacity      int
	LastHeartbeat time.Time
	Weight        int
}

// SchedulerTask is a lightweight task descriptor used by scheduling algorithms.
type SchedulerTask struct {
	ID          string
	Handler     string
	MatchLabels map[string]string
}

// ToSchedulerWorkerInfo converts a WorkerInfo (with gRPC state) into a
// SchedulerWorkerInfo suitable for scheduling algorithms.
func ToSchedulerWorkerInfo(w *WorkerInfo) *SchedulerWorkerInfo {
	labels := make(map[string]string)
	// Copy labels from discovery NodeInfo
	for k, v := range w.Registration.Labels {
		labels[k] = v
	}
	// Also copy from Metadata for backwards compatibility
	for k, v := range w.Registration.Metadata {
		// Skip internal keys used by worker_manager
		if k == "handlers" || k == "capacity" {
			continue
		}
		if _, exists := labels[k]; !exists {
			labels[k] = v
		}
	}

	weight := parseIntLabel(w.Registration.Metadata, "weight", 1)

	return &SchedulerWorkerInfo{
		ID:            w.Registration.ID,
		Addr:          w.Registration.Addr,
		Labels:        labels,
		ActiveTasks:   w.ActiveTasks,
		Capacity:      w.Capacity,
		LastHeartbeat: w.LastHeartbeat,
		Weight:        weight,
	}
}

// FilterByLabels filters workers to only those whose labels match ALL of the
// task's matchLabels. If the task has no matchLabels, all workers are returned.
// Returns ErrNoMatchingLabels if no worker matches.
func FilterByLabels(workers []*SchedulerWorkerInfo, matchLabels map[string]string) ([]*SchedulerWorkerInfo, error) {
	if len(matchLabels) == 0 {
		return workers, nil
	}

	var matched []*SchedulerWorkerInfo
	for _, w := range workers {
		if workerMatchesLabels(w, matchLabels) {
			matched = append(matched, w)
		}
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf("filter workers by labels %v: %w", matchLabels, ErrNoMatchingLabels)
	}
	return matched, nil
}

// workerMatchesLabels returns true if the worker has ALL the required labels.
func workerMatchesLabels(w *SchedulerWorkerInfo, matchLabels map[string]string) bool {
	for k, v := range matchLabels {
		wv, ok := w.Labels[k]
		if !ok || wv != v {
			return false
		}
	}
	return true
}

// filterAtCapacity removes workers that have reached their capacity limit.
func filterAtCapacity(workers []*SchedulerWorkerInfo) []*SchedulerWorkerInfo {
	var available []*SchedulerWorkerInfo
	for _, w := range workers {
		if w.ActiveTasks < w.Capacity {
			available = append(available, w)
		}
	}
	return available
}
