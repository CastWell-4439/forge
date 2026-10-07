package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// --- the permission gate (read < write < delete vs L0..L4) ---

// A read tool under the default authority runs: the gate must not block the
// ordinary case, or every deployment would start paused.
func TestGateAllowsReadUnderDefaultAuthority(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(readRegistry(t)), DefaultLoopConfig())

	decision, ok := loop.checkToolAuthority("test.read")
	require.True(t, ok, "a read tool is within L2: %s", decision.Reason)
	assert.Equal(t, core.EffectRead, decision.Effect)
}

// A write tool under the default (L2) authority is refused — and the decision
// names the tool, the effect and the authority it would need, because that
// message is what a human reads before releasing the run.
func TestGateRefusesWriteUnderDefaultAuthority(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(writeRegistry(t)), DefaultLoopConfig())

	decision, ok := loop.checkToolAuthority("test.write")
	require.False(t, ok, "L2 may not write")
	assert.Equal(t, core.EffectWrite, decision.Effect)
	assert.Equal(t, core.AuthorityL3, decision.Required)
	assert.Contains(t, decision.Reason, "test.write")
	assert.Contains(t, decision.Reason, "L3")
	assert.Contains(t, decision.Reason, "L2", "the message says what the run holds")
}

// Raising the run's authority is what unlocks a write; nothing about the tool
// changes. This is the ceiling at work.
func TestGateWriteAllowedAtL3(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL3
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(writeRegistry(t)), cfg)

	_, ok := loop.checkToolAuthority("test.write")
	assert.True(t, ok, "L3 covers writes")
}

// A delete tool needs the top rung: even L3 is refused, which is the
// distinction between "a mistake you can undo" and "gone".
func TestGateDeleteNeedsL4(t *testing.T) {
	for _, authority := range []core.Authority{core.AuthorityL2, core.AuthorityL3} {
		cfg := DefaultLoopConfig()
		cfg.Authority = authority
		loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
			NewToolRouter(deleteRegistry(t)), cfg)

		decision, ok := loop.checkToolAuthority("test.delete")
		require.False(t, ok, "%s must not delete", authority)
		assert.Equal(t, core.AuthorityL4, decision.Required)
	}

	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL4
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(deleteRegistry(t)), cfg)
	_, ok := loop.checkToolAuthority("test.delete")
	assert.True(t, ok, "L4 covers deletions")
}

// The task's declaration takes the STRONGER of the two: a task that says
// "delete" makes even a read tool wait, so a cautious pipeline is honoured.
func TestGateTaskDeclarationCanTighten(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL3
	cfg.TaskEffect = core.EffectDelete
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(readRegistry(t)), cfg)

	decision, ok := loop.checkToolAuthority("test.read")
	require.False(t, ok, "the task asked for delete-level caution")
	assert.Equal(t, core.EffectDelete, decision.Effect, "the stronger effect won")
}

// ...and a lenient task cannot WEAKEN what the tool does. This is the property
// that stops a workflow from waving a destructive tool through.
func TestGateLenientTaskCannotWeakenTool(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL3
	cfg.TaskEffect = core.EffectRead // "this task is harmless"
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(deleteRegistry(t)), cfg)

	decision, ok := loop.checkToolAuthority("test.delete")
	require.False(t, ok, "a read declaration must not lower a delete tool")
	assert.Equal(t, core.EffectDelete, decision.Effect)
}

// An undeclared tool is judged as write: cautious, but not so strict that it
// blocks the world. (Delete would break harmless tools; read would let
// destructive ones through.)
func TestGateUndeclaredToolIsWrite(t *testing.T) {
	registry := workers.NewToolRegistry()
	require.NoError(t, registry.Register(&workers.ToolDef{Name: "test.undeclared"},
		func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		}))

	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registry), DefaultLoopConfig())

	decision, ok := loop.checkToolAuthority("test.undeclared")
	require.False(t, ok, "undeclared is cautious")
	assert.Equal(t, core.EffectWrite, decision.Effect)
}

// Meta operations are never gated: a run must always be able to ask for the
// permission it lacks, and window management touches no world state.
func TestGateNeverBlocksMetaOperations(t *testing.T) {
	// L0: nothing may execute — but asking for help still works.
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL0
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), cfg)

	for _, name := range []string{"agent.pause", "agent.query", "context.remaining", "context.compact", "context.recall"} {
		_, ok := loop.checkToolAuthority(name)
		assert.True(t, ok, "%s must never be gated", name)
	}
}

// An unknown tool is a routing problem the model can fix, not a permission
// question a human should be woken for.
func TestGateIgnoresUnknownTools(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	_, ok := loop.checkToolAuthority("does.not.exist")
	assert.True(t, ok, "the router reports unknown tools; the gate stays out of it")
}

// --- pause (N4) ---

// A pause ends the run with Reason "paused" and the reason preserved: the
// person releasing it reads exactly that string.
func TestPauseEndsRunWithReason(t *testing.T) {
	llm := &capturingLLM{responses: []string{
		`{"thought":"need a human","action":{"name":"agent.pause","params":{"reason":"which branch should I merge into?"}}}`,
	}}
	store := newMemoryStore()
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	result, err := loop.Run(context.Background(), "pause-1", "merge something")
	require.NoError(t, err)

	assert.Equal(t, "paused", result.Reason)
	assert.Equal(t, "which branch should I merge into?", result.PauseReason)
	assert.Contains(t, result.Answer, "which branch should I merge into?")
}

// The pause is journaled and checkpointed, which is what makes it resumable
// rather than merely stopped.
func TestPauseIsJournaledAndCheckpointed(t *testing.T) {
	j := pathJournal(t, t.TempDir())
	llm := &capturingLLM{responses: []string{
		`{"thought":"stop","action":{"name":"agent.pause","params":{"reason":"approve the deletion"}}}`,
	}}
	store := newMemoryStore()
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetCheckpoint(store)
	loop.SetJournal(j)

	result, err := loop.Run(context.Background(), "pause-2", "delete things")
	require.NoError(t, err)
	require.Equal(t, "paused", result.Reason)

	events, err := j.ReadEvents(context.Background(), "pause-2")
	require.NoError(t, err)
	var paused *RunEvent
	for i := range events {
		if events[i].Type == EventRunPaused {
			paused = &events[i]
		}
	}
	require.NotNil(t, paused, "a run_paused event was journaled")
	assert.Equal(t, "approve the deletion", paused.Data["reason"])

	// The checkpoint carries the position, so Resume has somewhere to start.
	cp, err := store.Latest(context.Background(), "pause-2")
	require.NoError(t, err)
	assert.False(t, cp.Completed, "a paused run is not completed")
	assert.NotEmpty(t, cp.Messages)
}

// A pause without a reason is REFUSED with an observation: the model is told to
// say what it needs. Parking a run with nothing to decide would waste a human's
// attention and the run's context.
func TestPauseWithoutReasonIsRefused(t *testing.T) {
	llm := &capturingLLM{responses: []string{
		`{"thought":"stop","action":{"name":"agent.pause","params":{}}}`,
		finalAnswer,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	result, err := loop.Run(context.Background(), "pause-3", "do it")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason, "the run continued instead of pausing")
	require.GreaterOrEqual(t, len(llm.calls), 2)
	assert.Contains(t, joinedText(llm.calls[1]), "must state the 'reason' parameter",
		"the model was told what was missing")
}

// The gate's refusal also pauses, and its reason is written for the person who
// will decide — it names the tool, the effect and the authority.
func TestAuthorityRefusalPausesWithExplanation(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL2 // default: writes are out of reach
	llm := &capturingLLM{responses: []string{
		`{"thought":"write it","action":{"name":"test.write","params":{}}}`,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(writeRegistry(t)), cfg)

	result, err := loop.Run(context.Background(), "gate-1", "write a file")
	require.NoError(t, err, "a refusal is a pause, not an error")

	assert.Equal(t, "paused", result.Reason)
	assert.Contains(t, result.PauseReason, "test.write")
	assert.Contains(t, result.PauseReason, "L3", "the reason says what would unlock it")
}

// A run whose authority is high enough executes the same call with no pause:
// the gate is a threshold, not a fixed policy of "always ask".
func TestAuthoritySufficientToolRuns(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL3
	llm := &capturingLLM{responses: []string{
		`{"thought":"write it","action":{"name":"test.write","params":{}}}`,
		finalAnswer,
	}}
	registry := writeRegistry(t)
	loop := NewAgentLoop(llm, NewToolRouter(registry), cfg)

	result, err := loop.Run(context.Background(), "gate-2", "write a file")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
}

// A resumed paused run continues from where it stopped: the checkpoint written
// by the pause is what Resume loads.
func TestPausedRunResumes(t *testing.T) {
	store := newMemoryStore()
	first := &capturingLLM{responses: []string{
		`{"thought":"stop","action":{"name":"agent.pause","params":{"reason":"which environment?"}}}`,
	}}
	loop := NewAgentLoop(first, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	paused, err := loop.Run(context.Background(), "pause-4", "deploy it")
	require.NoError(t, err)
	require.Equal(t, "paused", paused.Reason)

	// A human answered; the run continues.
	second := &capturingLLM{responses: []string{finalAnswer}}
	resumed := NewAgentLoop(second, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	resumed.SetCheckpoint(store)

	result, err := resumed.Resume(context.Background(), "pause-4")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason, "the paused run resumed and finished")
	assert.False(t, strings.Contains(result.Reason, "paused"))
}

// --- query vs pause: one asks, the other stops ---

// agent.query is NOT intercepted as a pause: it dispatches to the handler,
// which owns the interactive channel, and the run carries on with the answer.
func TestQueryIsDispatchedNotPaused(t *testing.T) {
	asked := ""
	registry := workers.NewToolRegistry()
	require.NoError(t, registry.Register(workers.AgentQueryDef(),
		func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
			asked, _ = params["question"].(string)
			return map[string]interface{}{"answer": "production"}, nil
		}))

	llm := &capturingLLM{responses: []string{
		`{"thought":"ask","action":{"name":"agent.query","params":{"question":"which environment?"}}}`,
		finalAnswer,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())

	result, err := loop.Run(context.Background(), "query-1", "deploy")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason, "a query does not end the run")
	assert.Equal(t, "which environment?", asked, "the question reached the interactive channel")
	assert.Contains(t, joinedText(llm.calls[1]), "production", "the answer went back to the model")
}

// The two tools are distinct operations, and the registry keeps them apart.
func TestPauseAndQueryAreDistinctTools(t *testing.T) {
	assert.NotEqual(t, workers.AgentPauseName, workers.AgentQueryName)
	assert.Equal(t, "agent.pause", workers.AgentPauseName)
	assert.Equal(t, "agent.query", workers.AgentQueryName)

	// The loop recognises exactly these two as control tools.
	assert.True(t, isControlTool(workers.AgentPauseName))
	assert.True(t, isControlTool(workers.AgentQueryName))
	assert.False(t, isControlTool("file.read"))
}

// --- helpers ---

func readRegistry(t *testing.T) *workers.ToolRegistry {
	return toolWithEffect(t, "test.read", core.EffectRead)
}

func writeRegistry(t *testing.T) *workers.ToolRegistry {
	return toolWithEffect(t, "test.write", core.EffectWrite)
}

func deleteRegistry(t *testing.T) *workers.ToolRegistry {
	return toolWithEffect(t, "test.delete", core.EffectDelete)
}

func toolWithEffect(t *testing.T, name string, effect core.ToolEffect) *workers.ToolRegistry {
	t.Helper()
	registry := workers.NewToolRegistry()
	require.NoError(t, registry.Register(&workers.ToolDef{Name: name, Effect: effect, Idempotent: true},
		func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		}))
	return registry
}
