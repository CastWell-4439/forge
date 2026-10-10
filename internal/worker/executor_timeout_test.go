package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// A handler that blocks must be stopped by the deadline the coordinator sent.
//
// The timeout_ms field existed on the wire and was never read: a handler could
// run past its task deadline while the coordinator's sweeper failed the task
// underneath it, the work still going with nothing able to stop it. This is the
// test that says the field now does something.
func TestExecutorAppliesTheDispatchedTimeout(t *testing.T) {
	r := NewRegistry()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var cancelled bool
	r.Register("blocker", func(ctx context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		select {
		case <-ctx.Done():
			cancelled = true
			return nil, ctx.Err()
		case <-release:
			return map[string]interface{}{"finished": true}, nil
		}
	})

	exec := NewExecutor(r)
	start := time.Now()
	resp := exec.Execute(context.Background(), &forgev1.TaskRequest{
		TaskId:    "t-1",
		Handler:   "blocker",
		TimeoutMs: 50,
	})
	elapsed := time.Since(start)

	assert.False(t, resp.GetSuccess(), "a handler that ran out of time did not succeed")
	assert.True(t, cancelled, "the handler must see its context cancelled, not be abandoned")
	assert.Less(t, elapsed, 5*time.Second, "the deadline must be applied promptly")
	assert.Contains(t, resp.GetErrorMsg(), "deadline", "and the failure must say why")
}

// A zero timeout means no deadline, which is what the coordinator sends for a
// task that declared none. The handler must not be cut short.
func TestExecutorWithoutTimeoutLetsTheHandlerFinish(t *testing.T) {
	r := NewRegistry()
	r.Register("quick", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		time.Sleep(20 * time.Millisecond)
		return map[string]interface{}{"ok": true}, nil
	})

	exec := NewExecutor(r)
	resp := exec.Execute(context.Background(), &forgev1.TaskRequest{
		TaskId:    "t-2",
		Handler:   "quick",
		TimeoutMs: 0,
	})

	assert.True(t, resp.GetSuccess(), "no deadline means no interruption: %s", resp.GetErrorMsg())
}

// A handler that finishes well inside its deadline is unaffected.
func TestExecutorDeadlineDoesNotAffectFastHandlers(t *testing.T) {
	r := NewRegistry()
	r.Register("fast", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})

	exec := NewExecutor(r)
	resp := exec.Execute(context.Background(), &forgev1.TaskRequest{
		TaskId:    "t-3",
		Handler:   "fast",
		TimeoutMs: 60_000,
	})

	require.True(t, resp.GetSuccess())
}
