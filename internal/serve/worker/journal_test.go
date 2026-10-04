package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// readSpans parses the projected spans.jsonl for a run.
func readSpans(t *testing.T, root, runID string) []model.Span {
	t.Helper()
	data, err := os.ReadFile(storage.NewLayout(root).SpansFile(runID))
	require.NoError(t, err, "spans.jsonl must exist after tool activity")
	var spans []model.Span
	for _, line := range splitLines(data) {
		var s model.Span
		require.NoError(t, json.Unmarshal(line, &s), "span line must be JSON: %s", line)
		spans = append(spans, s)
	}
	return spans
}

// splitLines splits raw JSONL bytes into non-empty lines.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func TestRunJournalProjectsSpansWithAParentTree(t *testing.T) {
	t.Setenv(envRunsRoot, t.TempDir())
	j := buildRunJournal()
	require.NotNil(t, j, "the journal is on by default")

	ctx := context.Background()
	now := time.Now().UTC()
	run := func(ev harness.RunEvent) {
		ev.RunID = "span-run"
		ev.TS = now
		require.NoError(t, j.AppendEvent(ctx, ev))
	}

	run(harness.RunEvent{Type: harness.EventRunStarted})
	run(harness.RunEvent{Type: harness.EventStepStarted, Step: 0})
	started := harness.RunEvent{Type: harness.EventToolStarted, Step: 0, Tool: "shell.run"}
	started.Data = map[string]any{"id": "span-run-step-0-shell", "idempotent": false, "turn": "{}"}
	run(started)
	done := harness.RunEvent{Type: harness.EventToolCompleted, Step: 0, Tool: "shell.run"}
	done.Data = map[string]any{"id": "span-run-step-0-shell", "result": "ok"}
	run(done)
	run(harness.RunEvent{Type: harness.EventStepCompleted, Step: 0})

	spans := readSpans(t, os.Getenv(envRunsRoot), "span-run")
	require.Len(t, spans, 2, "one span per tool, one span per step")

	byID := map[string]model.Span{}
	for _, s := range spans {
		byID[s.ID] = s
	}

	tool, ok := byID["span-run-step-0-shell"]
	require.True(t, ok, "the tool span carries its ledger id")
	step, ok := byID["span-run-step-0"]
	require.True(t, ok, "the step span exists")

	assert.Equal(t, "span-run-step-0", tool.ParentID,
		"the tool span must hang under its step span — this is the tree")
	assert.Equal(t, "shell.run", tool.Name)
	assert.Equal(t, "ok", tool.Status)
	assert.Equal(t, now, tool.StartedAt, "the tool's start comes from its tool_started event")

	assert.Equal(t, "step 0", step.Name)
	assert.Equal(t, "ok", step.Status)
}

// A failing tool is visible as a failing span: observability must not lie.
func TestRunJournalProjectsToolFailures(t *testing.T) {
	t.Setenv(envRunsRoot, t.TempDir())
	j := buildRunJournal()
	require.NotNil(t, j)

	ctx := context.Background()
	now := time.Now().UTC()

	started := harness.RunEvent{RunID: "err-run", Type: harness.EventToolStarted, Step: 2,
		Tool: "shell.run", TS: now}
	started.Data = map[string]any{"id": "call-1", "idempotent": false, "turn": "{}"}
	require.NoError(t, j.AppendEvent(ctx, started))

	failed := harness.RunEvent{RunID: "err-run", Type: harness.EventToolCompleted, Step: 2,
		Tool: "shell.run", TS: now.Add(time.Second)}
	failed.Data = map[string]any{"id": "call-1", "result": "", "error": "exit code 1"}
	require.NoError(t, j.AppendEvent(ctx, failed))

	spans := readSpans(t, os.Getenv(envRunsRoot), "err-run")
	require.Len(t, spans, 1)
	assert.Equal(t, "error", spans[0].Status)
	assert.Equal(t, now, spans[0].StartedAt, "start time still comes from the started event")
}

// FORGE_JOURNAL=off turns the whole thing off, and the loop treats nil as off.
func TestBuildRunJournalCanBeDisabled(t *testing.T) {
	t.Setenv(envJournalEnabled, "off")
	assert.Nil(t, buildRunJournal())
}

// The journal file itself lands in the run tree, next to spans, in its own
// format — harness events, not model.Event audit lines.
func TestRunJournalWritesItsOwnFileInTheRunTree(t *testing.T) {
	runsRoot := t.TempDir()
	t.Setenv(envRunsRoot, runsRoot)
	j := buildRunJournal()
	require.NotNil(t, j)

	require.NoError(t, j.AppendEvent(context.Background(), harness.RunEvent{
		RunID: "file-run", Type: harness.EventRunStarted,
		Data: map[string]any{"messages": []map[string]any{{"role": "user", "content": "hi"}}},
	}))

	layout := storage.NewLayout(runsRoot)
	path := layout.JournalFile("file-run")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "journal.jsonl lives in the run directory")
	assert.FileExists(t, filepath.Clean(path))

	var ev harness.RunEvent
	require.NoError(t, json.Unmarshal(raw, &ev))
	assert.Equal(t, harness.EventRunStarted, ev.Type, "the journal keeps its own event vocabulary")
	assert.EqualValues(t, 1, ev.Seq, "the durable layer assigns seq before the file sees it")
}
