// Package worker — WorkerManager tracks registered workers using unique.Handle
// for memory-efficient deduplication of worker IDs and handler names.
package worker

import (
	"sync"
	"time"
	"unique"
)

// WorkerInfo holds metadata about a registered worker.
type WorkerInfo struct {
	ID           unique.Handle[string]
	Addr         string
	Capacity     int
	ActiveTasks  int
	Handlers     []unique.Handle[string] // deduplicated handler names
	LastSeen     time.Time
	Labels       map[string]string
}

// WorkerManager maintains a registry of active workers.
// It uses unique.Handle[string] for worker IDs and handler names to reduce
// memory allocation when many workers share the same handler names
// (e.g., "ai.tts", "video.encode" repeated across hundreds of workers).
type WorkerManager struct {
	mu      sync.RWMutex
	workers map[unique.Handle[string]]*WorkerInfo
}

// NewWorkerManager creates a new WorkerManager.
func NewWorkerManager() *WorkerManager {
	return &WorkerManager{
		workers: make(map[unique.Handle[string]]*WorkerInfo),
	}
}

// Register adds or updates a worker in the registry.
func (m *WorkerManager) Register(id, addr string, capacity int, handlers []string, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle := unique.Make(id)

	// Deduplicate handler names via unique.Handle.
	dedupHandlers := make([]unique.Handle[string], len(handlers))
	for i, h := range handlers {
		dedupHandlers[i] = unique.Make(h)
	}

	m.workers[handle] = &WorkerInfo{
		ID:       handle,
		Addr:     addr,
		Capacity: capacity,
		Handlers: dedupHandlers,
		LastSeen: time.Now(),
		Labels:   labels,
	}
}

// Heartbeat updates the LastSeen timestamp for a worker.
func (m *WorkerManager) Heartbeat(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle := unique.Make(id)
	info, ok := m.workers[handle]
	if !ok {
		return false
	}
	info.LastSeen = time.Now()
	return true
}

// Deregister removes a worker from the registry.
func (m *WorkerManager) Deregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workers, unique.Make(id))
}

// Get returns a worker's info by ID.
func (m *WorkerManager) Get(id string) (*WorkerInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.workers[unique.Make(id)]
	return info, ok
}

// FindByHandler returns all workers that support the given handler.
func (m *WorkerManager) FindByHandler(handler string) []*WorkerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	target := unique.Make(handler)
	var result []*WorkerInfo

	for _, info := range m.workers {
		for _, h := range info.Handlers {
			if h == target {
				result = append(result, info)
				break
			}
		}
	}
	return result
}

// ActiveWorkers returns all workers seen within the given duration.
func (m *WorkerManager) ActiveWorkers(within time.Duration) []*WorkerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().Add(-within)
	var result []*WorkerInfo
	for _, info := range m.workers {
		if info.LastSeen.After(cutoff) {
			result = append(result, info)
		}
	}
	return result
}

// Count returns the total number of registered workers.
func (m *WorkerManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.workers)
}

// WorkerID returns the string value from a unique.Handle.
// Convenience for display/logging.
func WorkerID(h unique.Handle[string]) string {
	return h.Value()
}
