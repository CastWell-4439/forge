package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathJournal is a FileJournal rooted in a temp dir, keyed by session id.
func pathJournal(t *testing.T, dir string) *FileJournal {
	t.Helper()
	return NewFileJournal(func(sessionID string) string {
		return filepath.Join(dir, sessionID+".jsonl")
	})
}

// ev builds a minimal event for journal tests.
func ev(runID, typ string, seq int64) RunEvent {
	return RunEvent{Seq: seq, RunID: runID, Type: typ, TS: time.Now().UTC()}
}

func TestFileJournalRoundtrip(t *testing.T) {
	ctx := context.Background()
	j := pathJournal(t, t.TempDir())

	require.NoError(t, j.AppendEvent(ctx, ev("s", EventRunStarted, 1)))
	require.NoError(t, j.AppendEvent(ctx, ev("s", EventStepStarted, 2)))
	require.NoError(t, j.AppendEvent(ctx, ev("s", EventRunCompleted, 3)))

	events, err := j.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, []string{EventRunStarted, EventStepStarted, EventRunCompleted},
		[]string{events[0].Type, events[1].Type, events[2].Type})
}

// An append-only file can end in a half-written line after a crash. That torn
// tail is the safe kind of loss and must be tolerated; corruption anywhere
// else is not, because rebuild would silently skip state.
func TestFileJournalToleratesTornTailButRefusesMidFileCorruption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j := pathJournal(t, dir)
	path := filepath.Join(dir, "s.jsonl")

	require.NoError(t, j.AppendEvent(ctx, ev("s", EventRunStarted, 1)))
	require.NoError(t, j.AppendEvent(ctx, ev("s", EventStepStarted, 2)))

	// A torn tail: the last line stops mid-JSON.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"seq":3,"run_id":"s","type":"step_`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	events, err := j.ReadEvents(ctx, "s")
	require.NoError(t, err, "a torn tail must be skipped, not fatal")
	assert.Len(t, events, 2)

	// Corruption in the middle: a garbage line between two good ones.
	good := "{\"seq\":1,\"run_id\":\"s\",\"type\":\"run_started\"}\n"
	good += "not json at all\n"
	good += "{\"seq\":2,\"run_id\":\"s\",\"type\":\"step_started\"}\n"
	require.NoError(t, os.WriteFile(path, []byte(good), 0o644))

	_, err = j.ReadEvents(ctx, "s")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrJournalGap)
}

func TestFileJournalMissingFileIsEmptyNotAnError(t *testing.T) {
	events, err := pathJournal(t, t.TempDir()).ReadEvents(context.Background(), "never-seen")
	require.NoError(t, err)
	assert.Empty(t, events)
}

// stubJournal is an in-memory Journal that fails on demand, standing in for a
// broken disk target in the durable fallback tiers.
type stubJournal struct {
	name   string
	events []RunEvent
	fail   bool
}

func (s *stubJournal) AppendEvent(_ context.Context, e RunEvent) error {
	if s.fail {
		return errors.New(s.name + " is broken")
	}
	s.events = append(s.events, e)
	return nil
}

func (s *stubJournal) ReadEvents(_ context.Context, sessionID string) ([]RunEvent, error) {
	var out []RunEvent
	for _, e := range s.events {
		if e.RunID == sessionID {
			out = append(out, e)
		}
	}
	sortEvents(out)
	return out, nil
}

// Tier 1: a transient failure is buffered, then flushed before the next
// event, so the accounting ends up complete and in order.
func TestDurableJournalBuffersAndFlushesAfterATransientFailure(t *testing.T) {
	ctx := context.Background()
	primary := &stubJournal{name: "primary", fail: true}
	d := NewDurableJournal(primary, nil, DurableJournalOptions{})

	err := d.AppendEvent(ctx, ev("s", EventRunStarted, 0))
	require.Error(t, err, "a buffered event must report that it is not durable yet")
	assert.Empty(t, primary.events)

	primary.fail = false
	require.NoError(t, d.AppendEvent(ctx, ev("s", EventStepStarted, 0)),
		"once the target recovers, writes succeed again")

	events, err := d.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, EventRunStarted, events[0].Type, "the buffered event must flush first, in order")
	assert.Equal(t, int64(1), events[0].Seq)
	assert.Equal(t, EventStepStarted, events[1].Type)
	assert.Equal(t, int64(2), events[1].Seq)
}

// Tier 2: a persistently broken primary switches the session to the fallback
// target, and the buffered tail lands there.
func TestDurableJournalSwitchesToTheFallbackTarget(t *testing.T) {
	ctx := context.Background()
	primary := &stubJournal{name: "primary", fail: true}
	fallback := &stubJournal{name: "fallback"}
	d := NewDurableJournal(primary, fallback, DurableJournalOptions{SwitchAfter: 2})

	require.Error(t, d.AppendEvent(ctx, ev("s", EventRunStarted, 0)))
	require.Error(t, d.AppendEvent(ctx, ev("s", EventStepStarted, 0)),
		"the second failure switches the target but the write still did not happen")
	require.NoError(t, d.AppendEvent(ctx, ev("s", EventToolStarted, 0)),
		"after the switch the fallback accepts writes")

	assert.Empty(t, primary.events, "the broken primary must stop receiving events")
	require.Len(t, fallback.events, 3, "buffered and new events all land in the fallback")
	for i, e := range fallback.events {
		assert.Equal(t, int64(i+1), e.Seq, "seq must stay contiguous across the switch")
	}
}

// Tier 3: when both targets fail and the buffer overflows, the lost events
// are marked — the next successful write starts with an explicit gap marker.
// Rebuild must then refuse: the journal is honest, not usable.
func TestDurableJournalMarksDroppedEventsAndRebuildRefuses(t *testing.T) {
	ctx := context.Background()
	primary := &stubJournal{name: "primary", fail: true}
	d := NewDurableJournal(primary, nil, DurableJournalOptions{BufferSize: 2, SwitchAfter: 1 << 30})

	require.Error(t, d.AppendEvent(ctx, ev("s", EventRunStarted, 0))) // buffered
	require.Error(t, d.AppendEvent(ctx, ev("s", EventStepStarted, 0)))
	require.Error(t, d.AppendEvent(ctx, ev("s", EventToolStarted, 0)))   // overflow: drop #1
	require.Error(t, d.AppendEvent(ctx, ev("s", EventToolCompleted, 0))) // overflow: drop #2

	primary.fail = false
	require.NoError(t, d.AppendEvent(ctx, ev("s", EventStepCompleted, 0)),
		"recovery must succeed and carry the gap marker")

	events, err := d.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.NotEmpty(t, events)

	// The marker is not first in seq order — it consumes its own fresh seq,
	// older buffered events sort ahead of it. What matters: exactly one
	// marker, carrying the dropped range, written before the new event.
	var markers []RunEvent
	for _, e := range events {
		if e.Type == EventJournalGap {
			markers = append(markers, e)
		}
	}
	require.Len(t, markers, 1, "the loss must be marked exactly once")
	assert.EqualValues(t, 1, markers[0].Data["gap_from"])
	assert.EqualValues(t, 2, markers[0].Data["gap_to"])

	_, err = RebuildCheckpoint(events)
	require.Error(t, err, "a journal with loss must not rebuild")
	assert.ErrorIs(t, err, ErrJournalGap,
		"the dropped run_started means no scope, which is exactly the refuse case")
}

// A restarted process must continue the session's numbering instead of
// renumbering events it never saw, which would look like corruption.
func TestDurableJournalContinuesSequenceAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := pathJournal(t, dir)

	first := NewDurableJournal(file, nil, DurableJournalOptions{})
	require.NoError(t, first.AppendEvent(ctx, ev("s", EventRunStarted, 0)))
	require.NoError(t, first.AppendEvent(ctx, ev("s", EventStepStarted, 0)))

	// "Restart": a brand-new durable journal over the same files.
	second := NewDurableJournal(pathJournal(t, dir), nil, DurableJournalOptions{})
	require.NoError(t, second.AppendEvent(ctx, ev("s", EventRunCompleted, 0)))

	events, err := second.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, int64(3), events[2].Seq, "seq must continue from what is on disk")
}

// The merge reads both copies and prefers the primary on a duplicate seq.
func TestDurableJournalMergesPrimaryAndFallback(t *testing.T) {
	ctx := context.Background()
	primary := &stubJournal{}
	primary.events = []RunEvent{ev("s", EventRunStarted, 1), ev("s", EventStepStarted, 3)}
	fallback := &stubJournal{}
	fallback.events = []RunEvent{ev("s", EventStepStarted, 3), ev("s", EventRunCompleted, 2)}
	d := NewDurableJournal(primary, fallback, DurableJournalOptions{})

	events, err := d.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.Len(t, events, 3, "the duplicate seq 3 must collapse to one event")
	assert.Equal(t, int64(1), events[0].Seq)
	assert.Equal(t, int64(2), events[1].Seq)
	assert.Equal(t, int64(3), events[2].Seq)
}

// A journal whose very first write was torn has nothing after it — that is
// the safe tail-break shape ("the event did not complete, nothing else
// exists"), so an empty read is correct. What must never happen is the
// fragment surviving to *concatenate* with the next event; the write side
// truncates it away, which the last test here pins.
func TestFileJournalTornOnlyTailReadsAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"seq\":1,\"run_id\":"), 0o644))

	events, err := pathJournal(t, dir).ReadEvents(context.Background(), "s")
	require.NoError(t, err, "a torn tail is a safe loss shape, not corruption")
	assert.Empty(t, events)
}

// The write side must never let a torn fragment merge with a later event: the
// fragment is truncated away before the next append, so no completed event
// can ever hide inside an unparseable line.
func TestFileJournalTruncatesTornTailBeforeAppending(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	j := pathJournal(t, dir)

	// A crashed write left a fragment, and a later event must land cleanly.
	require.NoError(t, os.WriteFile(path, []byte("{\"seq\":1,\"run_id\":\"s\",\"type\":\"tool_"), 0o644))
	require.NoError(t, j.AppendEvent(ctx, ev("s", EventToolCompleted, 2)))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, json.Valid(raw), "every line must be a complete JSON document: %s", raw)

	events, err := j.ReadEvents(ctx, "s")
	require.NoError(t, err)
	require.Len(t, events, 1, "the fragment is gone, the new event survives")
	assert.Equal(t, EventToolCompleted, events[0].Type)
}

// json import is kept honest: events travel through the journal as generic
// JSON, and Data must survive the round trip used by rebuild.
func TestRunEventDataSurvivesJSONRoundTrip(t *testing.T) {
	original := RunEvent{
		Seq: 7, RunID: "s", Type: EventStepCompleted, Step: 2,
		TS:   time.Now().UTC().Truncate(time.Second),
		Data: map[string]any{"turns": []map[string]any{{"role": "user", "content": "观察"}}},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var back RunEvent
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, original.Seq, back.Seq)
	assert.Equal(t, original.Step, back.Step)

	turns, err := decodeMessages(back.Data["turns"])
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "观察", turns[0].Content)
}
