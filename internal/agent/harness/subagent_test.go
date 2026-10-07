package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/structured"
)

// --- depth ---

// Depth is checked against the CALLER's depth. MaxDepth 1 means a depth-0
// caller may delegate and a depth-1 caller may not.
func TestDepthLimitRefusesNestedDelegation(t *testing.T) {
	cfg := core.NormalizeSubagentConfig(core.SubagentConfig{
		Mode:   core.SubagentIsolated,
		Limits: core.SubagentLimits{MaxDepth: 1},
	})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config: cfg,
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) {
			t.Fatal("a refused delegation must not build a child")
			return nil, nil, nil
		},
	})
	require.NotNil(t, runner)

	// Depth 0 may delegate (the factory would be reached); depth 1 may not.
	err := runner.checkDepth(1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depth limit")

	assert.NoError(t, runner.checkDepth(0), "the top-level agent may delegate")
}

// MaxDepth 0 forbids delegation entirely, including from the top level.
func TestDepthZeroForbidsAllDelegation(t *testing.T) {
	cfg := core.NormalizeSubagentConfig(core.SubagentConfig{
		Mode:   core.SubagentIsolated,
		Limits: core.SubagentLimits{MaxDepth: 0},
	})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  cfg,
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) { return nil, nil, nil },
	})
	require.NotNil(t, runner)
	assert.Error(t, runner.checkDepth(0), "even the top level is refused")
}

// Two levels allow a grandchild: the limit counts levels, so depth 2 is where a
// depth-2 caller is refused.
func TestDepthTwoAllowsGrandchild(t *testing.T) {
	cfg := core.NormalizeSubagentConfig(core.SubagentConfig{
		Mode:   core.SubagentIsolated,
		Limits: core.SubagentLimits{MaxDepth: 2},
	})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  cfg,
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) { return nil, nil, nil },
	})
	require.NotNil(t, runner)
	assert.NoError(t, runner.checkDepth(0))
	assert.NoError(t, runner.checkDepth(1), "a child may delegate when two levels are allowed")
	assert.Error(t, runner.checkDepth(2))
}

// --- off means no runner ---

// A disabled deployment gets no runner at all, which is what makes "off" free:
// there is nothing to register and nothing to check.
func TestOffBuildsNoRunner(t *testing.T) {
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  core.SubagentConfig{Mode: core.SubagentOff},
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) { return nil, nil, nil },
	})
	assert.Nil(t, runner)

	// A mode that is enabled but has no factory is also no runner: without a way
	// to build a child, delegation cannot work, and returning a runner that
	// always fails would be worse than returning nothing.
	runner = NewSubagentRunner(SubagentRunnerConfig{Config: core.SubagentConfig{Mode: core.SubagentIsolated}})
	assert.Nil(t, runner)
}

// --- child ids ---

// A child id names its parent, so lineage is readable from the id alone.
func TestChildIDNamesItsParent(t *testing.T) {
	assert.Equal(t, "root-sub-1", defaultChildID("root", 1))
	assert.Equal(t, "root-sub-7", defaultChildID("root", 7))
	assert.Equal(t, "session-sub-1", defaultChildID("", 1), "an empty parent still yields a usable id")
}

// --- report shape ---

// The report shape decides what crosses back. Under "answer" the steps are
// dropped entirely — that omission IS the isolation.
func TestChildStepsHonoursReportShape(t *testing.T) {
	steps := []StepRecord{
		{Step: 1, Thought: "look", Action: &structured.ToolCallRequest{Name: "file.read"}, Result: &core.ToolResult{Output: "content"}},
		{Step: 2, Thought: "done", Action: &structured.ToolCallRequest{Name: "file.write"}, Result: &core.ToolResult{Error: "denied"}},
		// A terminal step has no action: the model answered instead of calling a
		// tool. Converting it must not dereference a nil.
		{Step: 3, Thought: "finished", Answer: "the answer"},
	}

	// Summary: thought + action, no results.
	summary := childSteps(steps, false)
	require.Len(t, summary, 3)
	assert.Equal(t, "look", summary[0].Thought)
	assert.Equal(t, "file.read", summary[0].Action)
	assert.Empty(t, summary[0].Result, "summary carries no tool output")
	assert.Empty(t, summary[1].Result)
	assert.Equal(t, "finished", summary[2].Thought, "a terminal step survives conversion")
	assert.Empty(t, summary[2].Action, "and reports no action rather than panicking")

	// Steps: everything, including errors (a failure is part of the trace).
	full := childSteps(steps, true)
	require.Len(t, full, 3)
	assert.Equal(t, "content", full[0].Result)
	assert.Equal(t, "error: denied", full[1].Result, "an errored step reports its error")

	assert.Nil(t, childSteps(nil, true))
}

// --- depth in context ---

// Depth travels in the context, not in the runner: the same runner may serve a
// root run and a nested one, so a per-runner counter would be wrong the moment
// two runs overlap.
func TestDepthTravelsInContext(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, 0, SubagentDepthFrom(ctx), "the top level is depth 0")

	ctx = WithSubagentDepth(ctx, 2)
	assert.Equal(t, 2, SubagentDepthFrom(ctx))

	// Nesting replaces rather than accumulates: the runner sets depth+1 when it
	// starts a child, so the value is already absolute.
	ctx = WithSubagentDepth(ctx, 3)
	assert.Equal(t, 3, SubagentDepthFrom(ctx))

	// A context with no depth recorded reads as 0, which is what makes the
	// top-level agent's depth the implicit default rather than a required value.
	assert.Equal(t, 0, SubagentDepthFrom(context.TODO()))
}

// --- parent session ---

func TestParentSessionTravelsInContext(t *testing.T) {
	ctx := WithParentSession(context.Background(), "root-session")
	assert.Equal(t, "root-session", parentSessionFromContext(ctx))
	assert.Empty(t, parentSessionFromContext(context.Background()))
	assert.Empty(t, parentSessionFromContext(context.TODO()), "an unrecorded session is empty")
}

// --- concurrency bound ---

// The semaphore bounds how many children one parent runs at once. It is created
// once per runner, so the limit applies across delegations rather than per call.
func TestConcurrencySemaphoreSize(t *testing.T) {
	cfg := core.NormalizeSubagentConfig(core.SubagentConfig{
		Mode:   core.SubagentIsolated,
		Limits: core.SubagentLimits{MaxDepth: 1, MaxConcurrent: 3},
	})
	runner := NewSubagentRunner(SubagentRunnerConfig{
		Config:  cfg,
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) { return nil, nil, nil },
	})
	require.NotNil(t, runner)
	assert.Equal(t, 3, cap(runner.sem))

	// An unset concurrency falls back to the default rather than to zero (which
	// would deadlock: a zero-capacity channel blocks forever).
	cfg2 := core.NormalizeSubagentConfig(core.SubagentConfig{Mode: core.SubagentIsolated})
	runner2 := NewSubagentRunner(SubagentRunnerConfig{
		Config:  cfg2,
		Factory: func(context.Context, string, []string) (*AgentLoop, func(), error) { return nil, nil, nil },
	})
	require.NotNil(t, runner2)
	assert.Equal(t, core.DefaultSubagentMaxConcurrent, cap(runner2.sem))
}

// --- Continue vs Resume ---

// Continue is not Resume: Resume re-enters a stopped run with no new input,
// while Continue appends a new turn to a session that may have finished. The
// distinction is the whole reason continuable mode needs its own entry point.
func TestContinueAndResumeAreDifferentModes(t *testing.T) {
	assert.NotEqual(t, runResume, runContinue)
	assert.NotEqual(t, runFresh, runContinue)
	assert.NotEqual(t, runFresh, runResume)

	// The mode values are ordered so the zero value is the safe one (a fresh
	// run), which means a config that forgets to set one does not accidentally
	// resume or continue something.
	assert.Equal(t, runMode(0), runFresh)
}

var _ = strings.TrimSpace
