package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/storage"
)

// seedHistory writes a small workflow history and returns its id.
//
// The store is built directly rather than by running a workflow: this command
// reads events, and a test for a reader should not depend on the writer working.
//
// It CLOSES the store before returning. BoltDB takes an exclusive file lock, so
// leaving it open would make the command under test fail to open the same
// database — which is a real constraint of the embedded backend, not a test
// artefact, and pretending otherwise would hide it.
func seedHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	t.Setenv("FORGE_PG_DSN", "")
	t.Setenv("FORGE_BOLT_PATH", path)

	store, err := storage.NewBoltStorage(path)
	require.NoError(t, err)

	ctx := context.Background()
	const wfID = "wf-history-test"

	require.NoError(t, store.SaveWorkflow(ctx, &storage.Workflow{
		ID: wfID, Name: "demo", Status: storage.WorkflowStatusRunning,
	}))

	write := func(seq int64, taskID string, typ storage.EventType, payload string) {
		var raw json.RawMessage
		if payload != "" {
			raw = json.RawMessage(payload)
		}
		require.NoError(t, store.SaveEvent(ctx, &storage.Event{
			WorkflowID:  wfID,
			TaskID:      taskID,
			Type:        typ,
			Payload:     raw,
			SequenceNum: seq,
		}))
	}

	write(1, "", storage.EventWorkflowSubmitted, "")
	write(2, "", storage.EventWorkflowStarted, "")
	write(3, "t-1", storage.EventTaskScheduled, "")
	write(4, "t-1", storage.EventTaskStarted, "")
	write(5, "t-1", storage.EventTaskCompleted, `{"value":"ok"}`)
	write(6, "t-2", storage.EventTaskStarted, "")
	write(7, "t-2", storage.EventTaskFailed, `{"error":"boom"}`)
	write(8, "", storage.EventWorkflowFailed, `{"error":"boom"}`)

	require.NoError(t, store.Close(), "the command must be able to open the database")
	return wfID
}

// The command reports what the events add up to and prints the log.
func TestHistoryReconstructsState(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{wfID}, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	out := stdout.String()
	assert.Contains(t, out, wfID)
	assert.Contains(t, out, "FAILED", "the reconstructed status must be reported")
	assert.Contains(t, out, "events:")
	assert.Contains(t, out, "WORKFLOW_SUBMITTED", "the log itself is printed too")
	assert.Contains(t, out, "TASK_FAILED")
}

// --until reconstructs the state as of that point, which is the time-travel
// question: what did this look like before the failure?
//
// The printed log is truncated to match. Printing events the reconstruction
// excludes would make the report contradict itself — a summary saying RUNNING
// above a log showing the failure.
func TestHistoryUntilReconstructsAnEarlierState(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	// Up to the task completing, before the later failure.
	code := runHistory([]string{wfID, "--until", "5"}, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	out := stdout.String()
	assert.Contains(t, out, "up to seq 5", "the cutoff must be stated, not implied")
	assert.Contains(t, out, "status: RUNNING", "at that point the workflow had not yet failed")
	assert.NotContains(t, out, "TASK_FAILED", "later events must not appear")
	assert.NotContains(t, out, "WORKFLOW_FAILED")
	assert.Contains(t, out, "TASK_COMPLETED", "events within the cutoff are still shown")
}

// --json with --until reports the same cutoff, so a program gets what a person got.
func TestHistoryJSONWithUntil(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{wfID, "--json", "--until", "2"}, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &decoded))

	assert.Equal(t, float64(2), decoded["until"])
	state := decoded["state"].(map[string]any)
	assert.Equal(t, "RUNNING", state["Status"])
}

// --events prints only the log, for someone who wants the raw record.
func TestHistoryEventsOnly(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{wfID, "--events"}, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	out := stdout.String()
	assert.Contains(t, out, "SEQ", "the table header is there")
	assert.Contains(t, out, "WORKFLOW_FAILED")
	assert.NotContains(t, out, "  status:", "the state summary must be omitted")
}

// --json emits the same information for a program.
func TestHistoryJSON(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{wfID, "--json"}, &stdout, &stderr)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &decoded), "output: %s", stdout.String())

	assert.Equal(t, wfID, decoded["workflow_id"])
	assert.Equal(t, float64(8), decoded["event_count"])

	state, ok := decoded["state"].(map[string]any)
	require.True(t, ok, "the reconstructed state must be included")
	assert.Equal(t, "FAILED", state["Status"])
}

// A workflow with no events is reported as such, not as an empty success:
// "no events" and "wrong id" look identical in an empty table.
func TestHistoryReportsAWorkflowWithNoEvents(t *testing.T) {
	seedHistory(t) // sets up the environment

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{"wf-does-not-exist"}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "no recorded events")
}

// A missing id is a usage error, not a lookup.
func TestHistoryRequiresAWorkflowID(t *testing.T) {
	t.Setenv("FORGE_BOLT_PATH", filepath.Join(t.TempDir(), "x.db"))

	var stdout, stderr bytes.Buffer
	code := runHistory(nil, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "workflow id is required")
}

// An unknown option is refused rather than silently ignored: a typo must not
// produce output that looks like the requested report.
func TestHistoryRejectsUnknownOptions(t *testing.T) {
	t.Setenv("FORGE_BOLT_PATH", filepath.Join(t.TempDir(), "x.db"))

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{"wf-1", "--nonsense"}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "unknown option")
}

// --until needs a number, and refuses a bad one rather than guessing.
func TestHistoryValidatesUntil(t *testing.T) {
	t.Setenv("FORGE_BOLT_PATH", filepath.Join(t.TempDir(), "x.db"))

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{"wf-1", "--until", "not-a-number"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "not a sequence number")

	stdout.Reset()
	stderr.Reset()
	code = runHistory([]string{"wf-1", "--until"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "needs a sequence number")
}

// Two arguments where one is expected is refused, so a mistyped command does not
// silently report on the wrong workflow.
func TestHistoryRejectsExtraArguments(t *testing.T) {
	t.Setenv("FORGE_BOLT_PATH", filepath.Join(t.TempDir(), "x.db"))

	var stdout, stderr bytes.Buffer
	code := runHistory([]string{"wf-1", "wf-2"}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "unexpected extra argument")
}

// The usage text names the environment it reads, because a CLI that silently
// reads a different database than the reader expects wastes an afternoon.
func TestHistoryUsageNamesItsEnvironment(t *testing.T) {
	var sb strings.Builder
	historyUsage(&sb)
	out := sb.String()

	assert.Contains(t, out, "FORGE_PG_DSN")
	assert.Contains(t, out, "FORGE_BOLT_PATH")
}

// The dispatcher routes `forge history` to this command.
func TestRunDispatchesHistory(t *testing.T) {
	wfID := seedHistory(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"history", wfID, "--events"}, &stdout, &stderr)

	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "WORKFLOW_SUBMITTED")
}

// The top-level help lists the new command, so it is discoverable.
func TestUsageListsHistory(t *testing.T) {
	var sb strings.Builder
	usage(&sb)
	assert.Contains(t, sb.String(), "forge history")
}
