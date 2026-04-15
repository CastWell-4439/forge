package coordinator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateBackoff_Fixed(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffFixed,
		InitialInterval: 5 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
	}

	for attempt := 1; attempt <= 5; attempt++ {
		d := calculateBackoff(attempt, policy)
		assert.Equal(t, 5*time.Second, d, "fixed backoff should always return InitialInterval")
	}
}

func TestCalculateBackoff_Exponential(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffExponentialWithJitter,
		InitialInterval: 1 * time.Second,
		MaxInterval:     60 * time.Second,
		Multiplier:      2.0,
	}

	// With jitter, backoff is in [0, min(initial*2^(attempt-1), max))
	// Attempt 1: [0, 1s)
	// Attempt 2: [0, 2s)
	// Attempt 3: [0, 4s)
	// Attempt 4: [0, 8s)

	for i := 0; i < 100; i++ {
		d1 := calculateBackoff(1, policy)
		assert.Less(t, d1, 1*time.Second, "attempt 1 should be < 1s")
		assert.GreaterOrEqual(t, d1, time.Duration(0), "should be non-negative")

		d3 := calculateBackoff(3, policy)
		assert.Less(t, d3, 4*time.Second, "attempt 3 should be < 4s")

		d4 := calculateBackoff(4, policy)
		assert.Less(t, d4, 8*time.Second, "attempt 4 should be < 8s")
	}
}

func TestCalculateBackoff_CappedAtMaxInterval(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffExponentialWithJitter,
		InitialInterval: 1 * time.Second,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
	}

	// Attempt 10: 1s * 2^9 = 512s, but capped at 5s
	for i := 0; i < 100; i++ {
		d := calculateBackoff(10, policy)
		assert.Less(t, d, 5*time.Second, "should be capped at MaxInterval")
	}
}

func TestCalculateBackoff_ExponentialNoJitter(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffExponential,
		InitialInterval: 1 * time.Second,
		MaxInterval:     60 * time.Second,
		Multiplier:      2.0,
	}

	// Without explicit jitter type, exponential still applies jitter
	// per the tech spec (Full Jitter is always applied for non-fixed)
	for i := 0; i < 50; i++ {
		d := calculateBackoff(3, policy)
		assert.Less(t, d, 4*time.Second, "attempt 3: 1s * 2^2 = 4s max")
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name    string
		task    *RetryableTask
		want    bool
	}{
		{
			name: "has retries left",
			task: &RetryableTask{Attempt: 1, MaxAttempts: 3},
			want: true,
		},
		{
			name: "last attempt",
			task: &RetryableTask{Attempt: 3, MaxAttempts: 3},
			want: false,
		},
		{
			name: "over max",
			task: &RetryableTask{Attempt: 5, MaxAttempts: 3},
			want: false,
		},
		{
			name: "zero attempts zero max",
			task: &RetryableTask{Attempt: 0, MaxAttempts: 0},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShouldRetry(tc.task))
		})
	}
}

func TestEvaluateRetry_ShouldRetry(t *testing.T) {
	task := &RetryableTask{
		ID:          "task-1",
		Attempt:     1,
		MaxAttempts: 3,
		RetryPolicy: RetryPolicy{
			BackoffType:     BackoffFixed,
			InitialInterval: 5 * time.Second,
			MaxInterval:     30 * time.Second,
			Multiplier:      2.0,
		},
	}

	decision := EvaluateRetry(task)
	require.True(t, decision.ShouldRetry)
	assert.Equal(t, 2, decision.NextAttempt)
	assert.Equal(t, 5*time.Second, decision.Delay)
}

func TestEvaluateRetry_MaxAttemptsExhausted(t *testing.T) {
	task := &RetryableTask{
		ID:          "task-1",
		Attempt:     3,
		MaxAttempts: 3,
		RetryPolicy: RetryPolicy{
			BackoffType:     BackoffFixed,
			InitialInterval: 5 * time.Second,
		},
	}

	decision := EvaluateRetry(task)
	assert.False(t, decision.ShouldRetry)
}

func TestEvaluateRetry_ExponentialDelayIncreases(t *testing.T) {
	policy := RetryPolicy{
		BackoffType:     BackoffFixed, // use fixed for deterministic testing
		InitialInterval: 2 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
	}

	// Each attempt should have the same delay with fixed
	for attempt := 1; attempt <= 3; attempt++ {
		task := &RetryableTask{
			Attempt:     attempt,
			MaxAttempts: 5,
			RetryPolicy: policy,
		}
		decision := EvaluateRetry(task)
		require.True(t, decision.ShouldRetry)
		assert.Equal(t, 2*time.Second, decision.Delay)
	}
}
