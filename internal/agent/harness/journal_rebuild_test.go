package harness

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

var snapshot = []core.Message{
	{Role: "system", Content: "sys"},
	{Role: "user", Content: "do it"},
}

// mk builds a sequenced event, stamping the data bag.
func mk(seq int64, typ string, step int, data map[string]any) RunEvent {
	return RunEvent{
		Seq: seq, RunID: "s", Type: typ, Step: step,
		TS: time.Now().UTC(), Data: data,
	}
}

func toolStepEvents(seq *int64, step int, tool, turn string, idempotent bool, result string) []RunEvent {
	*seq++
	start := mk(*seq, EventToolStarted, step, map[string]any{
		"id": "call-" + tool, "idempotent": idempotent, "turn": turn,
	})
	start.Tool = tool
	*seq++
	done := mk(*seq, EventToolCompleted, step, map[string]any{
		"id": "call-" + tool, "result": result,
	})
	done.Tool = tool
	*seq++
	complete := mk(*seq, EventStepCompleted, step, map[string]any{
		"turns": []core.Message{
			{Role: "assistant", Content: turn},
			{Role: "user", Content: "[Tool \"" + tool + "\" result]: " + result},
		},
	})
	return []RunEvent{start, done, complete}
}

// The happy path: a completed run rebuilds as a completed checkpoint with the
// conversation intact — this is the property the whole design rests on.
func TestRebuildCompletesAFinishedRun(t *testing.T) {
	seq := int64(0)
	seq++
	events := []RunEvent{mk(seq, EventRunStarted, 0, map[string]any{
		"messages": snapshot, "input": "do it",
	})}
	events = append(events, toolStepEvents(&seq, 0, "test.push", `{"thought":"t","action":{"name":"test.push"}}`, false, "ok")...)
	seq++
	events = append(events, mk(seq, EventRunCompleted, 1, map[string]any{"answer": "finished"}))

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.True(t, cp.Completed)
	assert.Equal(t, "finished", cp.Answer)
	assert.Equal(t, 0, cp.StepIndex)
	require.Len(t, cp.Messages, 4, "snapshot + turn + observation")
	assert.Equal(t, "sys", cp.Messages[0].Content)
	assert.Contains(t, cp.Messages[2].Content, "test.push")
	require.Len(t, cp.ToolCalls, 1)
	assert.Equal(t, core.ToolCallCompleted, cp.ToolCalls[0].Status)
}

// A crashed mid-run journal rebuilds as an unfinished checkpoint at the last
// completed step, with the tool recorded so no side effect is replayed.
func TestRebuildResumesMidRun(t *testing.T) {
	seq := int64(0)
	seq++
	events := []RunEvent{mk(seq, EventRunStarted, 0, map[string]any{"messages": snapshot})}
	events = append(events, toolStepEvents(&seq, 0, "test.push", `{"thought":"t"}`, false, "ok")...)

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.False(t, cp.Completed)
	assert.Equal(t, 0, cp.StepIndex, "startStep becomes StepIndex+1 = 1, past the finished step")
	_, dangling := cp.UnresolvedToolCall()
	assert.False(t, dangling, "a completed tool must not look in-flight")
}

// The finest-grained case: the tool finished but the step never did. The
// conversation is reconstructed from the tool events instead of redoing the
// step — redoing it would repeat a side effect that already happened.
func TestRebuildReconstructsAPartialStepWithoutRedoingIt(t *testing.T) {
	seq := int64(0)
	seq++
	events := []RunEvent{mk(seq, EventRunStarted, 0, map[string]any{"messages": snapshot})}
	seq++
	start := mk(seq, EventToolStarted, 0, map[string]any{
		"id": "call-1", "idempotent": false, "turn": `{"thought":"t"}`,
	})
	start.Tool = "test.push"
	seq++
	done := mk(seq, EventToolCompleted, 0, map[string]any{
		"id": "call-1", "result": "ok",
	})
	done.Tool = "test.push"
	events = append(events, start, done) // no step_completed

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.Equal(t, 0, cp.StepIndex, "the step counts as done, so startStep moves past it")
	require.Len(t, cp.Messages, 4, "turn + observation reconstructed from tool events")
	assert.Equal(t, `{"thought":"t"}`, cp.Messages[2].Content)
	assert.Contains(t, cp.Messages[3].Content, "ok")
	require.Len(t, cp.ToolCalls, 1)
	assert.Equal(t, core.ToolCallCompleted, cp.ToolCalls[0].Status)
	_, dangling := cp.UnresolvedToolCall()
	assert.False(t, dangling)
}

// A tool in flight stays dangling: D-06's rules take over (refuse
// non-idempotent, redo idempotent) rather than being re-derived here.
func TestRebuildKeepsAnInFlightToolDangling(t *testing.T) {
	seq := int64(0)
	seq++
	events := []RunEvent{mk(seq, EventRunStarted, 0, map[string]any{"messages": snapshot})}
	seq++
	start := mk(seq, EventToolStarted, 0, map[string]any{
		"id": "call-1", "idempotent": false, "turn": `{"thought":"t"}`,
	})
	start.Tool = "test.push"
	events = append(events, start)

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	pending, ok := cp.UnresolvedToolCall()
	require.True(t, ok, "an in-flight tool must be visible to resume")
	assert.Equal(t, "test.push", pending.Tool)
	assert.False(t, pending.Idempotent)
}

// A seq jump means lost events. Rebuild refuses — a jump could be hiding a
// tool_started whose tool already ran.
func TestRebuildRefusesOnSeqJump(t *testing.T) {
	events := []RunEvent{
		mk(1, EventRunStarted, 0, map[string]any{"messages": snapshot}),
		mk(2, EventStepStarted, 0, nil),
		mk(4, EventRunCompleted, 1, map[string]any{"answer": "x"}), // seq 3 lost
	}
	_, err := RebuildCheckpoint(events)
	require.ErrorIs(t, err, ErrJournalGap)
}

// An explicit gap marker is the write side telling the read side about loss
// it already knows about — same refusal, better message.
func TestRebuildRefusesOnGapMarker(t *testing.T) {
	events := []RunEvent{
		mk(1, EventRunStarted, 0, map[string]any{"messages": snapshot}),
		mk(2, EventStepStarted, 0, nil),
		mk(3, EventJournalGap, 0, map[string]any{"gap_from": 2, "gap_to": 2}),
	}
	_, err := RebuildCheckpoint(events)
	require.ErrorIs(t, err, ErrJournalGap)
	assert.Contains(t, err.Error(), "loss")
}

// Events without a run start mean the start itself was lost — refuse rather
// than treat the stream as a clean slate.
func TestRebuildRefusesWhenRunStartIsMissing(t *testing.T) {
	events := []RunEvent{
		mk(5, EventStepStarted, 0, nil),
		mk(6, EventRunCompleted, 1, map[string]any{"answer": "x"}),
	}
	_, err := RebuildCheckpoint(events)
	require.ErrorIs(t, err, ErrJournalGap)
	assert.Contains(t, err.Error(), "run start is missing")
}

func TestRebuildWithoutEventsIsNotAnError(t *testing.T) {
	_, err := RebuildCheckpoint(nil)
	require.ErrorIs(t, err, ErrNoJournalEvents)
}

// The scope is the LAST run start: an older, even gappy run in the same
// journal is audit, not state. The resume snapshot supersedes everything
// before it.
func TestRebuildScopesToTheLastRunStartAndIgnoresOlderRuns(t *testing.T) {
	events := []RunEvent{
		mk(1, EventRunStarted, 0, map[string]any{"messages": snapshot}),
		mk(2, EventStepStarted, 0, nil),
		mk(9, EventStepStarted, 0, nil), // old run has a hole — irrelevant
		// resume snapshot: step 4 done, conversation restored
		mk(10, EventRunResumed, 0, map[string]any{
			"messages":   append([]core.Message{}, snapshot...),
			"step_index": 4,
			"source":     "checkpoint",
		}),
		mk(11, EventStepStarted, 5, nil),
	}

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.Equal(t, 4, cp.StepIndex, "the snapshot's step index, not anything the old run says")
	assert.False(t, cp.Completed)
}

// A finished run's journal answers resume without running anything — the
// journal-side twin of the completed-checkpoint short circuit.
func TestRebuildOfAResumedScopeAfterCompletionStaysCompleted(t *testing.T) {
	seq := int64(0)
	seq++
	events := []RunEvent{mk(seq, EventRunStarted, 0, map[string]any{"messages": snapshot})}
	seq++
	events = append(events, mk(seq, EventRunCompleted, 0, map[string]any{"answer": "done"}))

	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)
	assert.True(t, cp.Completed)
	assert.Equal(t, "done", cp.Answer)
}
