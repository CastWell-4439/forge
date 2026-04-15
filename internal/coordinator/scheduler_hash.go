package coordinator

import (
	"fmt"
	"hash/crc32"
	"sort"
)

const defaultVnodes = 150

// ConsistentHashScheduler routes tasks with the same handler to the same worker
// for cache locality. Uses a hash ring with virtual nodes.
type ConsistentHashScheduler struct {
	vnodes int
}

// NewConsistentHashScheduler creates a new Consistent Hash scheduler.
func NewConsistentHashScheduler() *ConsistentHashScheduler {
	return &ConsistentHashScheduler{vnodes: defaultVnodes}
}

// hashRingEntry represents a point on the hash ring.
type hashRingEntry struct {
	hash     uint32
	workerID string
}

// Schedule selects a worker by hashing the task's handler name onto a ring.
// If the target worker is at capacity, it walks the ring to find the next available.
func (s *ConsistentHashScheduler) Schedule(task *SchedulerTask, workers []*SchedulerWorkerInfo) (*SchedulerWorkerInfo, error) {
	// Filter by labels
	filtered, err := FilterByLabels(workers, task.MatchLabels)
	if err != nil {
		return nil, err
	}

	// Filter out workers at capacity
	available := filterAtCapacity(filtered)
	if len(available) == 0 {
		return nil, ErrNoWorkerAvailable
	}

	// Build worker lookup
	workerMap := make(map[string]*SchedulerWorkerInfo, len(available))
	for _, w := range available {
		workerMap[w.ID] = w
	}

	// Build hash ring with virtual nodes
	ring := s.buildRing(available)

	// Hash the task handler to find position on ring
	taskHash := crc32.ChecksumIEEE([]byte(task.Handler))

	// Binary search for the first ring entry >= taskHash
	idx := sort.Search(len(ring), func(i int) bool {
		return ring[i].hash >= taskHash
	})

	// Walk the ring from the found position to find an available worker
	n := len(ring)
	for i := 0; i < n; i++ {
		entry := ring[(idx+i)%n]
		if w, ok := workerMap[entry.workerID]; ok {
			return w, nil
		}
	}

	// All ring entries point to at-capacity workers (shouldn't happen since
	// we pre-filtered, but fallback just in case)
	return available[0], nil
}

// buildRing creates a sorted hash ring with virtual nodes for each worker.
func (s *ConsistentHashScheduler) buildRing(workers []*SchedulerWorkerInfo) []hashRingEntry {
	ring := make([]hashRingEntry, 0, len(workers)*s.vnodes)
	for _, w := range workers {
		for i := 0; i < s.vnodes; i++ {
			key := []byte(fmt.Sprintf("%s#%d", w.ID, i))
			h := crc32.ChecksumIEEE(key)
			ring = append(ring, hashRingEntry{hash: h, workerID: w.ID})
		}
	}
	sort.Slice(ring, func(i, j int) bool {
		return ring[i].hash < ring[j].hash
	})
	return ring
}
