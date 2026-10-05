package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// The fingerprint must not be fooled by parameter order and must change when
// the parameters change — everything below depends on both.
func TestToolFingerprintStableAcrossParamOrder(t *testing.T) {
	a := map[string]any{"b": 2, "a": 1}
	b := map[string]any{"a": 1, "b": 2}

	assert.Equal(t, toolFingerprint("tool", a), toolFingerprint("tool", b),
		"map insertion order must not change identity")
	assert.NotEqual(t, toolFingerprint("tool", a), toolFingerprint("tool", map[string]any{"a": 1}),
		"changed parameters must change identity")
	assert.NotEqual(t, toolFingerprint("tool", a), toolFingerprint("other", a),
		"changed tool must change identity")
}

// THE defect: a non-idempotent call whose twin already ran is refused before
// the handler — the ledger always claimed replaying it would double the side
// effect, and this is that claim made enforceable.
func TestDuplicateNonIdempotentCallRefused(t *testing.T) {
	registry, calls := countingRegistry(t, "test.push", false)
	llm := &capturingLLM{responses: []string{
		toolCall("test.push"),
		toolCall("test.push"), // identical twin
		finalAnswer,
	}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	result, err := loop.Run(context.Background(), "dup-nonidem", "push twice")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason, "a refusal is feedback, not a crash")
	assert.Equal(t, 1, *calls, "the handler must run exactly once")
	require.GreaterOrEqual(t, len(llm.calls), 3)
	observed := messageText(llm.calls[2])
	assert.Contains(t, observed, "duplicate call refused")
	assert.Contains(t, observed, "already ran in this run")
}

// Replaying an idempotent tool is harmless: it executes, and the streak (not
// execution) is what decides when to stop.
func TestIdempotentDuplicateStillExecutes(t *testing.T) {
	registry, calls := countingRegistry(t, "test.lookup", true)
	llm := &capturingLLM{responses: []string{
		toolCall("test.lookup"),
		toolCall("test.lookup"),
		finalAnswer,
	}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	result, err := loop.Run(context.Background(), "dup-idem", "read twice")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, 2, *calls, "idempotent reads are allowed to repeat")
}

// The streak stop: identical calls past the threshold end the run honestly,
// with everything the run did preserved — the max_steps family, not an error.
func TestNoProgressStopsRun(t *testing.T) {
	registry, calls := countingRegistry(t, "test.push", false)
	llm := &capturingLLM{responses: []string{
		toolCall("test.push"),
		toolCall("test.push"),
		// A refusal triggers Reflexion, which consumes one scripted response
		// as its "reflection" — the extra entry keeps the script's shape.
		toolCall("test.push"),
		toolCall("test.push"),
		finalAnswer, // never reached
	}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	result, err := loop.Run(context.Background(), "stuck", "loop")
	require.NoError(t, err)

	assert.Equal(t, "no_progress", result.Reason)
	require.Len(t, result.Steps, 3, "the steps that produced the verdict are kept")
	assert.Contains(t, result.Answer, "repeated")
	assert.Equal(t, 1, *calls, "the side effect ran once; the rest were refusals")
}

// The escape hatch: an explicit negative threshold disables the streak stop
// (used by tests whose subject is a different exit) without touching the
// duplicate guard.
func TestNoProgressThresholdExplicitlyDisabled(t *testing.T) {
	registry, calls := countingRegistry(t, "test.push", true)
	llm := &mockLLM{responses: []string{
		`{"thought": "go", "action": {"name": "test.push", "params": {"x": "y"}}}`,
	}}

	config := DefaultLoopConfig()
	config.MaxSteps = 2
	config.NoProgressThreshold = -1
	loop := NewAgentLoop(llm, NewToolRouter(registry), config)

	result, err := loop.Run(context.Background(), "off-switch", "loop")
	require.NoError(t, err)

	assert.Equal(t, "max_steps", result.Reason, "disabled streak exits via max_steps")
	assert.Equal(t, 2, *calls)
}

// State rebuilds from the ledger: completed non-idempotent calls count,
// idempotent and unfinished ones do not.
func TestInitProgressTrackingRestoresFromLedger(t *testing.T) {
	registry, _ := countingRegistry(t, "test.push", false)
	loop := NewAgentLoop(&mockLLM{}, NewToolRouter(registry), DefaultLoopConfig())

	loop.initProgressTracking([]core.ToolCallRecord{
		{Tool: "file.write", Params: map[string]any{"path": "a.txt"}, Status: core.ToolCallCompleted, Idempotent: false},
		{Tool: "file.read", Params: map[string]any{"path": "b.txt"}, Status: core.ToolCallCompleted, Idempotent: true},
		{Tool: "shell.run", Params: map[string]any{"cmd": "go test"}, Status: core.ToolCallStarted, Idempotent: false},
	})

	assert.True(t, loop.nonIdempotentDone[toolFingerprint("file.write", map[string]any{"path": "a.txt"})],
		"a completed non-idempotent call blocks its twin")
	assert.False(t, loop.nonIdempotentDone[toolFingerprint("file.read", map[string]any{"path": "b.txt"})],
		"idempotent reads are never blocked")
	assert.False(t, loop.nonIdempotentDone[toolFingerprint("shell.run", map[string]any{"cmd": "go test"})],
		"a call that never finished is the resume's unresolved case, not a completed one")
	assert.Zero(t, loop.noProgress, "a resumed run starts its streak fresh")
}

// Params ride the journal: a rebuild must be able to see what was called,
// which is also what the ledger's own comment promises an operator.
func TestRebuildRestoresToolParams(t *testing.T) {
	j := pathJournal(t, t.TempDir())
	registry, _ := countingRegistry(t, "test.push", false)
	llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetJournal(j)

	result, err := loop.Run(context.Background(), "params-rebuild", "do it")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	events, err := j.ReadEvents(context.Background(), "params-rebuild")
	require.NoError(t, err)
	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)

	require.NotEmpty(t, cp.ToolCalls, "the rebuilt ledger has the tool call")
	record := cp.ToolCalls[0]
	assert.Equal(t, "test.push", record.Tool)
	assert.NotEmpty(t, record.Params, "the rebuild sees what the tool was called with — it was written in the tool_started event")
}

// The crash-boundary case: a call that ran before the process died keeps its
// block AFTER a resume. This is why Params joined the record — without them a
// resume could not know which call it was.
func TestResumeRefusesToolThatRanBeforeCrash(t *testing.T) {
	ctx := context.Background()

	// Attempt 1: the tool runs, then the model call dies.
	store := &memoryStore{byID: map[string]*core.Checkpoint{}, latest: map[string]*core.Checkpoint{}}
	runRegistry, runCalls := countingRegistry(t, "test.push", false)
	interrupted := &failingLLM{responses: []string{toolCall("test.push")}}
	first := NewAgentLoop(interrupted, NewToolRouter(runRegistry), DefaultLoopConfig())
	first.SetCheckpoint(store)

	_, err := first.Run(ctx, "crash-session", "do it")
	require.Error(t, err, "the simulated interruption must surface")
	require.Equal(t, 1, *runCalls, "the tool ran before the crash")

	// Attempt 2: a fresh process, the same checkpoint, and a model that tries
	// the exact same call again.
	resumeRegistry, resumeCalls := countingRegistry(t, "test.push", false)
	resumedLLM := &capturingLLM{responses: []string{toolCall("test.push"), finalAnswer}}
	resumed := NewAgentLoop(resumedLLM, NewToolRouter(resumeRegistry), DefaultLoopConfig())
	resumed.SetCheckpoint(store)

	result, err := resumed.Resume(ctx, "crash-session")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Zero(t, *resumeCalls,
		"the side effect that ran before the crash must not run again after resume")
	require.GreaterOrEqual(t, len(resumedLLM.calls), 2)
	assert.Contains(t, messageText(resumedLLM.calls[1]), "duplicate call refused",
		"the model is told why the call was refused")
}
