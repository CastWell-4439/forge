package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskContextWithTimeout_WithTimeout(t *testing.T) {
	ctx := context.Background()
	tctx, cancel := TaskContextWithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	deadline, ok := tctx.Deadline()
	require.True(t, ok, "should have a deadline")
	assert.WithinDuration(t, time.Now().Add(100*time.Millisecond), deadline, 20*time.Millisecond)
}

func TestTaskContextWithTimeout_ZeroTimeout(t *testing.T) {
	ctx := context.Background()
	tctx, cancel := TaskContextWithTimeout(ctx, 0)
	defer cancel()

	_, ok := tctx.Deadline()
	assert.False(t, ok, "should not have a deadline with zero timeout")
}

func TestTaskContextWithTimeout_NegativeTimeout(t *testing.T) {
	ctx := context.Background()
	tctx, cancel := TaskContextWithTimeout(ctx, -1*time.Second)
	defer cancel()

	_, ok := tctx.Deadline()
	assert.False(t, ok, "should not have a deadline with negative timeout")
}

func TestTaskContextWithTimeout_Expires(t *testing.T) {
	ctx := context.Background()
	tctx, cancel := TaskContextWithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	select {
	case <-tctx.Done():
		assert.ErrorIs(t, tctx.Err(), context.DeadlineExceeded)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("context should have expired")
	}
}

func TestTimeoutManager_New(t *testing.T) {
	tm := NewTimeoutManager(nil)
	assert.NotNil(t, tm)
}
