package coordinator

import (
	"sync"
)

// WRRScheduler implements weighted round-robin scheduling.
// Workers with higher Weight values receive proportionally more tasks.
type WRRScheduler struct {
	mu      sync.Mutex
	current int // index of last-selected worker (by sorted ID)
	gcd     int // greatest common divisor of all weights
	maxW    int // maximum weight
	cw      int // current weight threshold
}

// NewWRRScheduler creates a new Weighted Round Robin scheduler.
func NewWRRScheduler() *WRRScheduler {
	return &WRRScheduler{
		current: -1,
	}
}

// Schedule selects the next worker using weighted round-robin.
// It filters out workers at capacity or with wrong labels before scheduling.
func (s *WRRScheduler) Schedule(task *SchedulerTask, workers []*SchedulerWorkerInfo) (*SchedulerWorkerInfo, error) {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	// Compute GCD and max weight for the current worker set
	g := available[0].Weight
	maxW := available[0].Weight
	for _, w := range available[1:] {
		g = gcd(g, w.Weight)
		if w.Weight > maxW {
			maxW = w.Weight
		}
	}
	s.gcd = g
	s.maxW = maxW

	n := len(available)
	// WRR algorithm: iterate at most n full rounds to find a candidate
	for i := 0; i < n*maxW; i++ {
		s.current = (s.current + 1) % n
		if s.current == 0 {
			s.cw -= s.gcd
			if s.cw <= 0 {
				s.cw = s.maxW
			}
		}
		if available[s.current].Weight >= s.cw {
			return available[s.current], nil
		}
	}

	// Fallback: should not reach here, but return first available
	return available[0], nil
}

// gcd computes the greatest common divisor of two non-negative integers.
func gcd(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}
