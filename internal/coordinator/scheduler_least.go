package coordinator

// LeastActiveScheduler assigns tasks to the worker with the fewest active tasks.
// Ties are broken by weight (higher weight wins).
type LeastActiveScheduler struct{}

// NewLeastActiveScheduler creates a new Least Active scheduler.
func NewLeastActiveScheduler() *LeastActiveScheduler {
	return &LeastActiveScheduler{}
}

// Schedule picks the worker with the lowest ActiveTasks count.
// On tie, the worker with the higher Weight wins.
func (s *LeastActiveScheduler) Schedule(task *SchedulerTask, workers []*SchedulerWorkerInfo) (*SchedulerWorkerInfo, error) {
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

	best := available[0]
	for _, w := range available[1:] {
		if w.ActiveTasks < best.ActiveTasks {
			best = w
		} else if w.ActiveTasks == best.ActiveTasks && w.Weight > best.Weight {
			best = w
		}
	}

	return best, nil
}
