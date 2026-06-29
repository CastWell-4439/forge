package stop

// RetryState tracks how many retries each error fingerprint has consumed. It is
// keyed by fingerprint so unrelated failures keep independent budgets. RetryState
// is not safe for concurrent use; the Engine guards access with its own lock.
type RetryState struct {
	Counts map[string]int
}

// NewRetryState returns an initialized, empty RetryState.
func NewRetryState() *RetryState {
	return &RetryState{Counts: make(map[string]int)}
}

// Count returns the number of retries recorded for a fingerprint. An empty
// fingerprint (or a nil/empty state) always reports zero.
func (s *RetryState) Count(fingerprint string) int {
	if s == nil || s.Counts == nil || fingerprint == "" {
		return 0
	}
	return s.Counts[fingerprint]
}

// Inc increments the retry counter for a fingerprint. Empty fingerprints are
// ignored so callers never panic on unclassified errors.
func (s *RetryState) Inc(fingerprint string) {
	if s == nil || fingerprint == "" {
		return
	}
	if s.Counts == nil {
		s.Counts = make(map[string]int)
	}
	s.Counts[fingerprint]++
}
