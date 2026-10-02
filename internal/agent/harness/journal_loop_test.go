package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/workers"
)

// A full run leaves a complete, ordered journal, and the rebuild of it is a
// completed checkpoint carrying the conversation.
func TestRunJournalsEveryStageAndRebuildCompletes(t *testing.T) {
	ctx := context.Background()
	j := pathJournal(t, t.TempDir())
	registry, calls := countingRegistry(t, "test.push", false)
	llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}

	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetJournal(j)

	result, err := loop.Run(ctx, "journal-run", "do it")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)
	require.Equal(t, 1, *calls)

	events, err := j.ReadEvents(ctx, "journal-run")
	require.NoError(t, err)
	require.NotEmpty(t, events)

	// Seq is contiguous from the very first event of the run.
	for i, e := range events {
		require.Equal(t, int64(i+1), e.Seq, "journal seq must be contiguous")
	}

	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	assert.Equal(t, []string{
		EventRunStarted,
		EventStepStarted, EventLLMCall, EventToolStarted, EventToolCompleted, EventStepCompleted,
		EventStepStarted, EventLLMCall,
		EventRunCompleted,
	}, types)

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.True(t, cp.Completed)
	assert.Equal(t, "finished", cp.Answer)
	assert.Len(t, cp.Messages, 4, "the rebuilt conversation includes the tool exchange")
}

// The end-to-end slow path: a run dies with NO checkpoint store at all, and
// the journal alone rebuilds enough state to finish the run — without
// repeating the tool that already ran.
func TestResumeRebuildsFromJournalWhenCheckpointIsMissing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j := pathJournal(t, dir)
	registry, calls := countingRegistry(t, "test.push", false)

	// First attempt: one tool call, then the model call dies. Journal only.
	interrupted := &failingLLM{responses: []string{toolCall("test.push")}}
	first := NewAgentLoop(interrupted, NewToolRouter(registry), DefaultLoopConfig())
	first.SetJournal(j)
	if _, err := first.Run(ctx, "journal-x", "do it"); err == nil {
		t.Fatal("the interrupted run should have reported an error")
	}
	require.Equal(t, 1, *calls)

	// Restart: new loop, new tool counter, same journal, still no checkpoint.
	restartedRegistry, restartedCalls := countingRegistry(t, "test.push", false)
	resumedLLM := &capturingLLM{responses: []string{finalAnswer}}
	resumed := NewAgentLoop(resumedLLM, NewToolRouter(restartedRegistry), DefaultLoopConfig())
	resumed.SetJournal(pathJournal(t, dir)) // a fresh instance over the same files

	result, err := resumed.Resume(ctx, "journal-x")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, "finished", result.Answer)
	assert.Equal(t, 0, *restartedCalls, "a journal rebuild must not repeat a completed tool")
	require.Len(t, resumedLLM.calls, 1, "the run must continue, not restart")
	assert.Greater(t, len(resumedLLM.calls[0]), 2,
		"the rebuilt conversation must carry the earlier exchange")
}

// A finished run answers from the journal without touching the model —
// the journal-side twin of the completed-checkpoint short circuit.
func TestResumeReturnsTheAnswerRebuiltFromTheJournal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j := pathJournal(t, dir)
	registry, calls := countingRegistry(t, "test.push", false)
	llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}

	first := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	first.SetJournal(j)
	_, err := first.Run(ctx, "journal-done", "do it")
	require.NoError(t, err)
	require.Equal(t, 1, *calls)

	idle := &capturingLLM{responses: []string{finalAnswer}}
	resumed := NewAgentLoop(idle, NewToolRouter(registry), DefaultLoopConfig())
	resumed.SetJournal(pathJournal(t, dir))

	result, err := resumed.Resume(ctx, "journal-done")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, "finished", result.Answer)
	assert.Empty(t, idle.calls, "a finished session must not run again")
	assert.Equal(t, 1, *calls, "and no tool may be invoked again")
}

// A journal with a hole refuses to resume: without knowing what happened in
// the gap, running could repeat a side effect. This is D-06's judgement,
// applied on the read side.
func TestResumeRefusesWhenTheJournalHasAGap(t *testing.T) {
	ctx := context.Background()
	j := pathJournal(t, t.TempDir())

	// seq 1 then seq 3: something in between is gone.
	require.NoError(t, j.AppendEvent(ctx, RunEvent{
		Seq: 1, RunID: "gap-s", Type: EventRunStarted,
		Data: map[string]any{"messages": snapshot},
	}))
	require.NoError(t, j.AppendEvent(ctx, RunEvent{Seq: 3, RunID: "gap-s", Type: EventStepStarted, Step: 1}))

	registry, calls := countingRegistry(t, "test.push", false)
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetJournal(j)

	_, err := loop.Resume(ctx, "gap-s")
	require.Error(t, err, "a gappy journal must refuse instead of guessing")
	assert.ErrorIs(t, err, ErrJournalGap)
	assert.Empty(t, llm.calls, "the refusal must come before any model call")
	assert.Equal(t, 0, *calls, "and before any tool call")
}

// Journal write failures follow the same policy switch as checkpoints:
// best-effort logs and continues, strict refuses to call a run successful.
func TestJournalFailureFollowsTheCheckpointPolicy(t *testing.T) {
	ctx := context.Background()
	broken := &stubJournal{name: "broken", fail: true}

	cases := []struct {
		name   string
		policy CheckpointFailurePolicy
		wantOK bool
	}{
		{"best effort", CheckpointBestEffort, true},
		{"strict", CheckpointStrict, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, _ := countingRegistry(t, "test.push", false)
			llm := &mockLLM{responses: []string{toolCall("test.push"), finalAnswer}}
			loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
			loop.SetJournal(broken)
			loop.SetCheckpointFailurePolicy(tc.policy)

			_, err := loop.Run(ctx, "policy-s", "do it")
			if tc.wantOK {
				require.NoError(t, err, "best effort must not fail the run over journaling")
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "broken", "strict must surface the write failure")
			}
		})
	}
}

// A journal read failure refuses resume: an unreadable journal is
// indistinguishable from a lost one, and "lost" is not a clean slate.
func TestResumeRefusesWhenTheJournalCannotBeRead(t *testing.T) {
	loop := NewAgentLoop(&capturingLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetJournal(&failingReadJournal{})

	_, err := loop.Resume(context.Background(), "unreadable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read journal")
}

type failingReadJournal struct{}

func (f *failingReadJournal) AppendEvent(context.Context, RunEvent) error { return nil }
func (f *failingReadJournal) ReadEvents(context.Context, string) ([]RunEvent, error) {
	return nil, errors.New("io: disk gone")
}
