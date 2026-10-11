package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// The background path is checked with a factory that returns a working child
// loop. The child answers immediately unless the test deliberately blocks it,
// so what is under test is the register's bookkeeping rather than the child.

// answerChild builds a child loop that finishes at once.
//
// The response carries `thought`, which the structured parser requires: a reply
// with only an answer is rejected, so omitting it would make every child fail
// for a reason unrelated to what these tests check.
func answerChild(answer string) func(context.Context, string, []string) (*AgentLoop, func(), error) {
	return func(_ context.Context, _ string, _ []string) (*AgentLoop, func(), error) {
		loop := NewAgentLoop(
			&mockLLM{responses: []string{`{"thought": "t", "answer": "` + answer + `"}`}},
			NewToolRouter(workers.NewToolRegistry()),
			DefaultLoopConfig(),
		)
		return loop, func() {}, nil
	}
}

// blockingChild builds a child whose model blocks until released, so a child can
// be observed while it is still running.
func blockingChild(release <-chan struct{}) func(context.Context, string, []string) (*AgentLoop, func(), error) {
	return func(_ context.Context, _ string, _ []string) (*AgentLoop, func(), error) {
		loop := NewAgentLoop(
			&blockingLLM{release: release, answer: `{"thought": "t", "answer": "done"}`},
			NewToolRouter(workers.NewToolRegistry()),
			DefaultLoopConfig(),
		)
		return loop, func() {}, nil
	}
}

// subagentCfg is the smallest configuration that permits delegation.
//
// MaxDepth must be set explicitly: zero forbids delegation (it is how a
// deployment registers the tool while refusing every call), so a test that left
// it at the zero value would be testing the refusal path by accident.
func subagentCfg() core.SubagentConfig {
	return core.SubagentConfig{
		Mode:   core.SubagentContinuable,
		Limits: core.SubagentLimits{MaxDepth: core.DefaultSubagentMaxDepth},
	}
}

// Dispatching must not wait for the child. That is the whole difference from
// RunSubagent, so it is asserted first.
func TestBackgroundDispatchReturnsImmediately(t *testing.T) {
	gate := make(chan struct{})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})
	reg := NewBackgroundRegistry(0)

	start := time.Now()
	task, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "work"}, &reg)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Less(t, elapsed, 500*time.Millisecond,
		"dispatch returns without waiting for the child to finish")

	close(gate)
	result, err := task.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", result.Answer)
}

// A dispatched task is findable while it runs, and collecting does not remove
// it: the caller removes when it is done with the result. Keeping those apart is
// what lets a caller ask "what happened to that one" after collecting.
//
// The child is held open so the register can be read while it is genuinely
// outstanding. With an instant child the count would already be back to zero and
// the test would pass without observing anything.
func TestBackgroundRegistryLifecycle(t *testing.T) {
	gate := make(chan struct{})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})
	reg := NewBackgroundRegistry(0)

	task, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "t"}, &reg)
	require.NoError(t, err)

	assert.Same(t, task, reg.Get(task.ChildID), "the task is findable by its child id")
	assert.Equal(t, 1, reg.ActiveCount(), "and it counts as outstanding while it runs")
	assert.False(t, task.Done(), "an uncollected running child is not done")

	close(gate)
	result, err := task.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", result.Answer)

	assert.NotNil(t, reg.Get(task.ChildID), "collecting does not remove it")
	reg.Remove(task.ChildID)
	assert.Nil(t, reg.Get(task.ChildID))
	assert.Equal(t, 0, reg.ActiveCount())
}

// The limit bounds outstanding children per register, and exceeding it is a
// refusal that names the number — not a queue, because a caller that silently
// waits has no way to learn it is over budget.
func TestBackgroundLimitRefusesRatherThanQueues(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})
	reg := NewBackgroundRegistry(2)

	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "a"}, &reg)
	require.NoError(t, err)
	_, err = runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "b"}, &reg)
	require.NoError(t, err)

	_, err = runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "c"}, &reg)
	require.Error(t, err, "the third must be refused, not queued")
	assert.Contains(t, err.Error(), "2", "and the limit is named so the caller can act")

	assert.Equal(t, 2, reg.ActiveCount())
}

// A register is per run: one run filling its budget does not block another.
func TestRegistersAreIndependent(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})

	first := NewBackgroundRegistry(1)
	second := NewBackgroundRegistry(1)

	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "a"}, &first)
	require.NoError(t, err)

	_, err = runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "b"}, &second)
	assert.NoError(t, err, "a register is per run, so one run's budget does not block another's")

	_, err = runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "c"}, &second)
	assert.Error(t, err, "but each register still enforces its own")
}

// Dispatch without a register is refused: an unowned background child is a leak,
// which is the failure this design exists to prevent.
func TestBackgroundRequiresARegistry(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: answerChild("ok")})

	var none *BackgroundRegistry
	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "x"}, &none)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry")
}

// Background does not bypass the depth rule: a caller that may not delegate
// synchronously may not delegate in the background either. Async is a scheduling
// choice, not a wider permission.
func TestBackgroundRespectsDepthLimit(t *testing.T) {
	cfg := subagentCfg()
	cfg.Limits.MaxDepth = 1
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: cfg, Factory: answerChild("ok")})
	reg := NewBackgroundRegistry(0)

	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "x", Depth: 0}, &reg)
	require.NoError(t, err)

	_, err = runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "y", Depth: 1}, &reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depth limit")
}

// Continuation in the background is refused: the child's session does not exist
// yet, so "continue the background task" would mean it had already been told to
// stop.
func TestBackgroundRefusesContinuation(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: answerChild("ok")})
	reg := NewBackgroundRegistry(0)

	_, err := runner.StartSubagent(context.Background(),
		core.SubagentRequest{Task: "x", ChildID: "existing-child"}, &reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "continuation")
}

// CollectAll drains everything, which is what a run does as it ends so nothing
// is left standing.
func TestCollectAllDrainsTheRegister(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: answerChild("ok")})
	reg := NewBackgroundRegistry(0)

	first, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "a"}, &reg)
	require.NoError(t, err)
	second, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "b"}, &reg)
	require.NoError(t, err)

	require.Len(t, reg.Tasks(), 2)

	results := reg.CollectAll(context.Background())
	assert.Len(t, results, 2)
	assert.Contains(t, results, first.ChildID)
	assert.Contains(t, results, second.ChildID)
	assert.Empty(t, reg.Tasks(), "nothing is left outstanding")
	assert.Equal(t, 0, reg.ActiveCount())
}

// CancelAll stops what is running and empties the register.
func TestCancelAllStopsEverything(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})
	reg := NewBackgroundRegistry(0)

	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "a"}, &reg)
	require.NoError(t, err)
	require.Equal(t, 1, reg.ActiveCount())

	// CancelAll must not wait on the child's model: the release function is what
	// stops a child, and the register is emptied without blocking on it.
	done := make(chan struct{})
	go func() {
		reg.CancelAll(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CancelAll did not return; it must not wait on a blocked child")
	}

	assert.Empty(t, reg.Tasks())
}

// Tasks() is ordered, so two readings of the same register agree.
func TestTasksAreOrdered(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: answerChild("ok")})
	reg := NewBackgroundRegistry(0)

	for _, name := range []string{"c", "a", "b"} {
		_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: name}, &reg)
		require.NoError(t, err)
	}

	ids := make([]string, 0, 3)
	for _, task := range reg.Tasks() {
		ids = append(ids, task.ChildID)
	}
	require.Len(t, ids, 3)
	assert.True(t, ids[0] < ids[1] && ids[1] < ids[2], "ids come back ordered: %v", ids)
}

// Done reports the child's state without blocking, so a caller can poll.
func TestDoneDoesNotBlock(t *testing.T) {
	gate := make(chan struct{})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  subagentCfg(),
		Factory: blockingChild(gate),
	})
	reg := NewBackgroundRegistry(0)

	task, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "t"}, &reg)
	require.NoError(t, err)

	// While the child is held open, Done answers false immediately rather than
	// waiting for it.
	start := time.Now()
	assert.False(t, task.Done())
	assert.Less(t, time.Since(start), 100*time.Millisecond, "Done must not block")

	close(gate)
	_, err = task.Collect(context.Background())
	require.NoError(t, err)
	assert.True(t, task.Done())
}

// Cancel is idempotent: a run that unwinds twice must not fail the second time.
func TestCancelIsIdempotent(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: answerChild("ok")})
	reg := NewBackgroundRegistry(0)

	task, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "t"}, &reg)
	require.NoError(t, err)

	require.NoError(t, task.Cancel(context.Background()))
	assert.NoError(t, task.Cancel(context.Background()), "the second cancel is a no-op, not an error")
}

// blockingLLM blocks until released, so a child can be observed mid-flight.
type blockingLLM struct {
	release <-chan struct{}
	answer  string
}

func (b *blockingLLM) Chat(ctx context.Context, _ []core.Message) (string, error) {
	select {
	case <-b.release:
		return b.answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (b *blockingLLM) ChatWithUsage(ctx context.Context, m []core.Message) (core.ChatResult, error) {
	content, err := b.Chat(ctx, m)
	return core.ChatResult{Content: content}, err
}
