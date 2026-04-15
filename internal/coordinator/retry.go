package coordinator

import (
	"log"
	"math"
	"math/rand"
	"time"
)

// calculateBackoff computes the retry delay for the given attempt number
// according to the retry policy. Implements Full Jitter as described in
// tech spec section 7.2.
func calculateBackoff(attempt int, policy RetryPolicy) time.Duration {
	if policy.BackoffType == BackoffFixed {
		return policy.InitialInterval
	}

	// Exponential backoff
	backoff := float64(policy.InitialInterval) * math.Pow(policy.Multiplier, float64(attempt-1))

	// Cap at max_interval
	if time.Duration(backoff) > policy.MaxInterval {
		backoff = float64(policy.MaxInterval)
	}

	// Full Jitter: [0, backoff)
	jittered := time.Duration(rand.Float64() * backoff)

	return jittered
}

// ShouldRetry checks whether a failed task should be retried based on its
// current attempt count and the retry policy from its DAG definition.
func ShouldRetry(task *RetryableTask) bool {
	return task.Attempt < task.MaxAttempts
}

// RetryableTask holds the minimal information needed to make retry decisions.
type RetryableTask struct {
	ID          string
	TaskName    string
	WorkflowID  string
	Handler     string
	Attempt     int
	MaxAttempts int
	RetryPolicy RetryPolicy
}

// RetryDecision represents the outcome of evaluating whether to retry a task.
type RetryDecision struct {
	ShouldRetry bool
	Delay       time.Duration
	NextAttempt int
}

// EvaluateRetry determines whether a task should be retried and with what delay.
func EvaluateRetry(task *RetryableTask) RetryDecision {
	if task.Attempt >= task.MaxAttempts {
		return RetryDecision{ShouldRetry: false}
	}

	nextAttempt := task.Attempt + 1
	delay := calculateBackoff(nextAttempt, task.RetryPolicy)

	return RetryDecision{
		ShouldRetry: true,
		Delay:       delay,
		NextAttempt: nextAttempt,
	}
}

// LogDeadLetter logs a permanently failed task for manual inspection.
// In a production system this would write to a dead letter queue; for now
// it uses structured logging so operators can filter and investigate.
func LogDeadLetter(task *RetryableTask, lastError string) {
	log.Printf("DEAD_LETTER: task=%s name=%s workflow=%s handler=%s attempts=%d/%d error=%s",
		task.ID, task.TaskName, task.WorkflowID, task.Handler,
		task.Attempt, task.MaxAttempts, lastError)
}
