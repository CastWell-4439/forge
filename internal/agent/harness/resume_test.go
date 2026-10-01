package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// memoryStore is a minimal CheckpointStore for loop tests. It keeps the newest
// checkpoint per session, which is all the loop asks of a store, and can be told
// to fail writes so the failure policy is testable.
type memoryStore struct {
	mu     sync.Mutex
	byID   map[string]*core.Checkpoint
	latest map[string]*core.Checkpoint
	saves  int
	err    error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		byID:   make(map[string]*core.Checkpoint),
		latest: make(map[string]*core.Checkpoint),
	}
}

func (s *memoryStore) Save(_ context.Context, cp *core.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.err != nil {
		return s.err
	}
	stored := *cp
	s.byID[cp.ID] = &stored
	if current, ok := s.latest[cp.SessionID]; !ok || cp.StepIndex >= current.StepIndex {
		s.latest[cp.SessionID] = &stored
	}
	return nil
}

func (s *memoryStore) Load(_ context.Context, id string) (*core.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %q: %w", id, core.ErrNoCheckpoint)
	}
	return cp, nil
}

func (s *memoryStore) Latest(_ context.Context, sessionID string) (*core.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.latest[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %q: %w", sessionID, core.ErrNoCheckpoint)
	}
	return cp, nil
}

// capturingLLM records the messages each call received. The loop keeps appending
// to the slice it hands over, so the copies matter.
type capturingLLM struct {
	responses []string
	calls     [][]core.Message
	idx       int
}

func (c *capturingLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := c.ChatWithUsage(ctx, messages)
	return result.Content, err
}

func (c *capturingLLM) ChatWithUsage(_ context.Context, messages []core.Message) (core.ChatResult, error) {
	recorded := make([]core.Message, len(messages))
	copy(recorded, messages)
	c.calls = append(c.calls, recorded)

	if c.idx >= len(c.responses) {
		return core.ChatResult{Content: c.responses[len(c.responses)-1]}, nil
	}
	response := c.responses[c.idx]
	c.idx++
	return core.ChatResult{Content: response}, nil
}

// failingLLM plays its script and then fails, standing in for a model call that
// never came back.
type failingLLM struct {
	responses []string
	idx       int
}

func (f *failingLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := f.ChatWithUsage(ctx, messages)
	return result.Content, err
}

func (f *failingLLM) ChatWithUsage(_ context.Context, _ []core.Message) (core.ChatResult, error) {
	if f.idx >= len(f.responses) {
		return core.ChatResult{}, errors.New("simulated interruption")
	}
	response := f.responses[f.idx]
	f.idx++
	return core.ChatResult{Content: response}, nil
}

func toolCall(name string) string {
	return fmt.Sprintf(`{"thought": "use %s", "action": {"name": %q, "params": {"input": "x"}}}`, name, name)
}

const finalAnswer = `{"thought": "done", "answer": "finished"}`

// countingRegistry registers one tool and returns a counter of its invocations.
func countingRegistry(t *testing.T, name string, idempotent bool) (*workers.ToolRegistry, *int) {
	t.Helper()
	calls := 0
	registry := workers.NewToolRegistry()
	err := registry.Register(&workers.ToolDef{
		Name:        name,
		Description: "test tool",
		Idempotent:  idempotent,
	}, func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		calls++
		return map[string]interface{}{"output": "ok"}, nil
	})
	require.NoError(t, err)
	return registry, &calls
}

// TestResumeContinuesAfterAnInterruption is the end-to-end case: a run dies
// after a tool call, and resuming finishes it without calling that tool again.
func TestResumeContinuesAfterAnInterruption(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	registry, calls := countingRegistry(t, "test.push", false)

	// The first attempt handles one tool call, then the model call fails.
	interrupted := &failingLLM{responses: []string{toolCall("test.push")}}
	first := NewAgentLoop(interrupted, NewToolRouter(registry), DefaultLoopConfig())
	first.SetCheckpoint(store)

	if _, err := first.Run(ctx, "session-x", "do it"); err == nil {
		t.Fatal("the interrupted run should have reported an error")
	}
	require.Equal(t, 1, *calls, "the tool should have run exactly once before the interruption")

	// A restart: a new loop, a new router, the same store.
	restartedRegistry, restartedCalls := countingRegistry(t, "test.push", false)
	resumedLLM := &capturingLLM{responses: []string{finalAnswer}}
	resumed := NewAgentLoop(resumedLLM, NewToolRouter(restartedRegistry), DefaultLoopConfig())
	resumed.SetCheckpoint(store)

	result, err := resumed.Resume(ctx, "session-x")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, "finished", result.Answer)
	assert.Equal(t, 0, *restartedCalls, "a resumed run must not repeat a tool that already completed")

	// The resumed run must see the earlier conversation, not a blank slate.
	require.Len(t, resumedLLM.calls, 1)
	assert.Greater(t, len(resumedLLM.calls[0]), 2,
		"the resumed run should have restored the saved messages, not restarted from system+user")
}

// TestResumeReturnsTheAnswerOfAFinishedRun makes resume idempotent for a session
// that already delivered its result.
func TestResumeReturnsTheAnswerOfAFinishedRun(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	registry, calls := countingRegistry(t, "test.push", false)
	llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	if _, err := loop.Run(ctx, "session-done", "do it"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	require.Equal(t, 1, *calls)

	// Resuming the same session must not run anything at all.
	idle := &capturingLLM{responses: []string{finalAnswer}}
	resumed := NewAgentLoop(idle, NewToolRouter(registry), DefaultLoopConfig())
	resumed.SetCheckpoint(store)

	result, err := resumed.Resume(ctx, "session-done")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, "finished", result.Answer)
	assert.Empty(t, idle.calls, "a finished session must not be run again")
	assert.Equal(t, 1, *calls, "and no tool may be invoked again")
}

// TestResumeRefusesToReplayANonIdempotentTool covers the case the ledger exists
// for: a tool was in flight when the process stopped, and re-running it could
// repeat its side effect.
func TestResumeRefusesToReplayANonIdempotentTool(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	require.NoError(t, store.Save(ctx, &core.Checkpoint{
		ID:        "session-y-step-0",
		SessionID: "session-y",
		StepIndex: 0,
		Messages:  []core.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "do it"}},
		ToolCalls: []core.ToolCallRecord{{
			ID: "call-1", StepIndex: 0, Tool: "test.push",
			Idempotent: false, Status: core.ToolCallStarted,
		}},
	}))

	registry, calls := countingRegistry(t, "test.push", false)
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	result, err := loop.Resume(ctx, "session-y")
	require.NoError(t, err)
	assert.Equal(t, "unresolved_side_effect", result.Reason)
	assert.Contains(t, result.Answer, "test.push")
	assert.Equal(t, 0, *calls, "a non-idempotent tool must never be replayed")
	assert.Empty(t, llm.calls, "the run should stop before asking the model again")
}

// TestResumeReplaysAnIdempotentTool is the other half of that decision.
func TestResumeReplaysAnIdempotentTool(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	require.NoError(t, store.Save(ctx, &core.Checkpoint{
		ID:        "session-z-step-0",
		SessionID: "session-z",
		StepIndex: 0,
		Messages:  []core.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "do it"}},
		ToolCalls: []core.ToolCallRecord{{
			ID: "call-1", StepIndex: 0, Tool: "test.lookup",
			Idempotent: true, Status: core.ToolCallStarted,
		}},
	}))

	registry, calls := countingRegistry(t, "test.lookup", true)
	llm := &mockLLM{responses: []string{toolCall("test.lookup"), finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	result, err := loop.Resume(ctx, "session-z")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, 1, *calls, "an idempotent tool may be replayed")
}

func TestResumeWithoutACheckpointStartsFresh(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	registry := workers.NewToolRegistry()
	llm := &capturingLLM{responses: []string{finalAnswer}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetCheckpoint(store)

	result, err := loop.Resume(ctx, "never-seen")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Len(t, llm.calls, 1, "an unknown session should run normally")
}

func TestResumeWithoutAStoreIsAnError(t *testing.T) {
	registry := workers.NewToolRegistry()
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(registry), DefaultLoopConfig())

	if _, err := loop.Resume(context.Background(), "s"); err == nil {
		t.Error("Resume without a checkpoint store should fail rather than silently restart")
	}
}

// TestCheckpointFailurePolicy pins the decision the failure policy exists to
// make: a run that cannot be checkpointed may either warn or refuse to continue.
func TestCheckpointFailurePolicy(t *testing.T) {
	ctx := context.Background()
	writeErr := errors.New("disk is full")

	cases := []struct {
		name    string
		policy  CheckpointFailurePolicy
		wantErr bool
	}{
		{"best effort", CheckpointBestEffort, false},
		{"strict", CheckpointStrict, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			store.err = writeErr

			registry, _ := countingRegistry(t, "test.push", false)
			llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}
			loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
			loop.SetCheckpoint(store)
			loop.SetCheckpointFailurePolicy(tc.policy)

			_, err := loop.Run(ctx, "session-f", "do it")
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "disk is full")
			} else {
				require.NoError(t, err)
			}
			assert.Greater(t, store.saves, 0, "the store should have been asked to save")
		})
	}
}
