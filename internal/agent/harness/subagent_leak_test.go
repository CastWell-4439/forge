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

// The leak guard, asserted directly: a run that ends with work still outstanding
// must not leave it running.
//
// This is the property the register exists for. A background child whose parent
// finished has nobody to report to — its result would be collected by no one and
// its release would be called by no one — so the run's end has to stop it. The
// test drives the same call the agent makes on its way out (CancelAll) and
// checks the child actually stopped rather than merely being forgotten.
func TestRunEndCancelsOutstandingChildren(t *testing.T) {
	// Buffered, because the child reports its cancellation with a NON-blocking
	// send: on an unbuffered channel that send finds no receiver ready and drops
	// the signal, and the test would report "never cancelled" about a child that
	// was cancelled correctly.
	stopped := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)

	// A child that would run forever if nothing stopped it. It reports when its
	// context is cancelled, which is the only thing that can stop it.
	factory := func(ctx context.Context, _ string, _ []string) (*AgentLoop, func(), error) {
		loop := NewAgentLoop(
			&cancellableLLM{stopped: stopped},
			NewToolRouter(workers.NewToolRegistry()),
			DefaultLoopConfig(),
		)
		return loop, func() {}, nil
	}

	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: factory})
	reg := NewBackgroundRegistry(0)

	_, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "long"}, &reg)
	require.NoError(t, err)
	require.Equal(t, 1, reg.ActiveCount())

	// The run ends. CancelAll is what Agent.Run defers.
	reg.CancelAll(context.Background())

	assert.Empty(t, reg.Tasks(), "the register is emptied")
	assert.Equal(t, 0, reg.ActiveCount())

	// And the child was actually stopped, not merely dropped from the register.
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the child was never cancelled: the register forgot it but it kept running")
	}
}

// A cancelled task collects as an error rather than as an empty success: "it was
// stopped" and "it finished with nothing" are different facts.
func TestCancelledChildCollectsAsAnError(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	factory := func(ctx context.Context, _ string, _ []string) (*AgentLoop, func(), error) {
		loop := NewAgentLoop(
			&cancellableLLM{stopped: make(chan struct{}, 1)},
			NewToolRouter(workers.NewToolRegistry()),
			DefaultLoopConfig(),
		)
		return loop, func() {}, nil
	}

	runner := NewSubagentRunner(SubagentRunnerConfig{Config: subagentCfg(), Factory: factory})
	reg := NewBackgroundRegistry(0)

	task, err := runner.StartSubagent(context.Background(), core.SubagentRequest{Task: "long"}, &reg)
	require.NoError(t, err)

	require.NoError(t, task.Cancel(context.Background()))

	_, err = task.Collect(context.Background())
	assert.Error(t, err, "a cancelled child has no result; reporting one would be a fabrication")
}

// cancellableLLM blocks until its context is cancelled, then reports that it was
// stopped. It models the only thing a long-running child can be doing.
type cancellableLLM struct {
	stopped chan struct{}
}

func (c *cancellableLLM) Chat(ctx context.Context, _ []core.Message) (string, error) {
	<-ctx.Done()
	select {
	case c.stopped <- struct{}{}:
	default:
	}
	return "", ctx.Err()
}

func (c *cancellableLLM) ChatWithUsage(ctx context.Context, m []core.Message) (core.ChatResult, error) {
	content, err := c.Chat(ctx, m)
	return core.ChatResult{Content: content}, err
}
