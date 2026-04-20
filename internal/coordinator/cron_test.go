package coordinator

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Cron expression parser tests ---

func TestParseCronExprValid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"every minute", "* * * * *"},
		{"every 5 minutes", "*/5 * * * *"},
		{"specific minute", "30 * * * *"},
		{"9am daily", "0 9 * * *"},
		{"weekdays 9am", "0 9 * * 1-5"},
		{"multiple values", "0,15,30,45 * * * *"},
		{"range", "0 9-17 * * *"},
		{"complex", "*/10 9-17 * 1-6 1-5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cron, err := parseCronExpr(tc.expr)
			require.NoError(t, err)
			assert.NotNil(t, cron)
		})
	}
}

func TestParseCronExprInvalid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"too few fields", "* * *"},
		{"too many fields", "* * * * * *"},
		{"invalid minute", "60 * * * *"},
		{"invalid hour", "* 25 * * *"},
		{"invalid step", "*/0 * * * *"},
		{"invalid range", "* 5-3 * * *"},
		{"non-numeric", "* abc * * *"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCronExpr(tc.expr)
			assert.Error(t, err)
		})
	}
}

func TestNextCronTime(t *testing.T) {
	// "every 5 minutes" after 14:02 → should be 14:05
	base := time.Date(2026, 4, 20, 14, 2, 0, 0, time.UTC)
	next, err := nextCronTime("*/5 * * * *", base)
	require.NoError(t, err)
	assert.Equal(t, 5, next.Minute())
	assert.Equal(t, 14, next.Hour())
}

func TestNextCronTimeEveryMinute(t *testing.T) {
	base := time.Date(2026, 4, 20, 14, 30, 0, 0, time.UTC)
	next, err := nextCronTime("* * * * *", base)
	require.NoError(t, err)
	assert.Equal(t, 31, next.Minute())
}

func TestNextCronTimeSpecificHour(t *testing.T) {
	// "0 9 * * *" (9am daily) after 10:00 → next day 9:00
	base := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	next, err := nextCronTime("0 9 * * *", base)
	require.NoError(t, err)
	assert.Equal(t, 9, next.Hour())
	assert.Equal(t, 0, next.Minute())
	assert.Equal(t, 21, next.Day())
}

func TestNextCronTimeWeekday(t *testing.T) {
	// Monday is weekday 1. 2026-04-20 is a Monday.
	base := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	next, err := nextCronTime("0 9 * * 1", base) // Monday 9am
	require.NoError(t, err)
	assert.Equal(t, time.Monday, next.Weekday())
	assert.Equal(t, 9, next.Hour())
}

func TestCronSchedulerAddRemove(t *testing.T) {
	coord := NewCoordinator(nil)
	sched := NewCronScheduler(coord, nil)

	err := sched.AddTrigger(&CronTrigger{
		ID:           "t1",
		WorkflowName: "test-wf",
		CronExpr:     "*/5 * * * *",
		Enabled:      true,
	})
	require.NoError(t, err)
	assert.Len(t, sched.Triggers(), 1)
	assert.NotNil(t, sched.Triggers()[0].NextFireAt)

	sched.RemoveTrigger("t1")
	assert.Len(t, sched.Triggers(), 0)
}

func TestCronSchedulerInvalidExpr(t *testing.T) {
	coord := NewCoordinator(nil)
	sched := NewCronScheduler(coord, nil)

	err := sched.AddTrigger(&CronTrigger{
		ID:       "bad",
		CronExpr: "invalid",
		Enabled:  true,
	})
	assert.Error(t, err)
}

// --- Timing wheel tests ---

func TestTimingWheelBasic(t *testing.T) {
	tw := NewTimingWheel(10*time.Millisecond, 100)
	tw.Start()
	defer tw.Stop()

	var fired atomic.Int32
	tw.Add(50*time.Millisecond, func() { fired.Add(1) })

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load())
}

func TestTimingWheelMultiple(t *testing.T) {
	tw := NewTimingWheel(10*time.Millisecond, 100)
	tw.Start()
	defer tw.Stop()

	var mu sync.Mutex
	var order []int

	tw.Add(30*time.Millisecond, func() { mu.Lock(); order = append(order, 1); mu.Unlock() })
	tw.Add(60*time.Millisecond, func() { mu.Lock(); order = append(order, 2); mu.Unlock() })
	tw.Add(90*time.Millisecond, func() { mu.Lock(); order = append(order, 3); mu.Unlock() })

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, []int{1, 2, 3}, order)
	mu.Unlock()
}

func TestTimingWheelPrecision(t *testing.T) {
	// E4B.4: 1000 tasks with reasonable error.
	// Use 10ms tick for Windows compatibility (1ms tickers are unreliable on Windows).
	tw := NewTimingWheel(10*time.Millisecond, 200)
	tw.Start()
	defer tw.Stop()

	const count = 1000
	var wg sync.WaitGroup
	wg.Add(count)

	errors := make([]time.Duration, count)
	targets := make([]time.Time, count)
	now := time.Now()

	for i := 0; i < count; i++ {
		delay := time.Duration(100+i) * time.Millisecond
		targets[i] = now.Add(delay)
		idx := i
		tw.Add(delay, func() {
			errors[idx] = time.Since(targets[idx])
			wg.Done()
		})
	}

	wg.Wait()

	var maxError time.Duration
	for _, err := range errors {
		if err < 0 {
			err = -err
		}
		if err > maxError {
			maxError = err
		}
	}

	// Tolerance: < 100ms under normal load.
	// Under extreme load (parallel test runs), allow up to 200ms.
	assert.Less(t, maxError, 200*time.Millisecond, "max timing error %v exceeds 200ms", maxError)
	t.Logf("1000 timers: max error = %v", maxError)
}

func TestTimingWheelPending(t *testing.T) {
	tw := NewTimingWheel(10*time.Millisecond, 100)

	tw.Add(50*time.Millisecond, func() {})
	tw.Add(60*time.Millisecond, func() {})

	assert.Equal(t, 2, tw.Pending())
}

func TestHierarchicalTimingWheel(t *testing.T) {
	tw := NewHierarchicalTimingWheel()
	tw.Start()
	defer tw.Stop()

	var fired atomic.Int32
	// Short delay: should be in level 0.
	tw.Add(50*time.Millisecond, func() { fired.Add(1) })

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load())
}
