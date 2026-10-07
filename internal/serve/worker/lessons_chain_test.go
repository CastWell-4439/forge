package worker

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/forgex/model"
	forgexruntime "github.com/castwell/forge/internal/forgex/runtime"
	forgestorage "github.com/castwell/forge/internal/storage"
)

// The whole F3 chain, end to end, with the real components: the runtime
// observer is handed a failed workflow, records the terminal failure and a
// classified error, derives the lesson from the run snapshot, persists it,
// auto-indexes it, and the agent-side source then recalls it.
//
// Each unit is tested on its own elsewhere; this test exists because the units
// can all pass while the CHAIN is broken — which is exactly what happened: the
// observer wrote <root>/index.db while the feed read a different default path,
// so both switches could be on and recall would still return nothing.
//
// Derive's own precondition is deliberate and worth restating: a lesson needs
// BOTH a halting stop decision AND at least one classified error. A run that
// merely "failed" without a classified error teaches nothing, so the test has
// to produce a real error envelope — that is the shape production has.
func TestLessonsChainObserverToRecall(t *testing.T) {
	root := t.TempDir()
	workflowID := "wf-chain-1"
	ctx := context.Background()

	observer := forgexruntime.NewFileObserver(forgexruntime.FileObserverConfig{
		Root:      root,
		AutoIndex: true, // the observer indexes the terminal run
		Authority: "L0",
	})

	// Seed the run directory the way the runtime does, then drive the terminal
	// event: saveEvent writes every artifact before the observer sees the
	// terminal one in production.
	runID := seedFailedRun(t, observer, workflowID)
	runDir := observer.Store().Layout().RunDir(runID)

	// The lesson reached disk, in the run's own directory. lessons.jsonl is
	// JSONL: one lesson object per line.
	lessonsPath := filepath.Join(runDir, "lessons.jsonl")
	rawLessons, err := os.ReadFile(lessonsPath)
	require.NoError(t, err, "the observer derived and persisted a lesson at %s", lessonsPath)

	var lesson map[string]any
	require.NoError(t, json.Unmarshal(firstJSONLine(t, rawLessons), &lesson))
	require.NotEmpty(t, lesson, "a failed run with a classified error yields a lesson")
	assert.NotEmpty(t, lesson["id"])
	assert.Equal(t, runID, lesson["source_run_id"])

	// And it is recallable through the agent-side interface, using the DEFAULT
	// index path — the one the feed resolves when FORGEX_INDEX_DB is unset.
	t.Setenv(envLessonsFeed, "on")
	t.Setenv(envRuntimeRoot, root)
	t.Setenv(envIndexDB, "") // force the default path, which is what broke
	t.Setenv(observerEnabledEnv, "on")
	t.Setenv(autoIndexEnv, "on")

	source := buildLessonSource()
	require.NotNil(t, source, "the feed is on and the index exists")
	defer func() {
		if closer, ok := source.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	items, err := source.Recall(ctx, "contract", 5)
	require.NoError(t, err)
	require.NotEmpty(t, items, "the lesson written by the observer is recallable through the feed")
	assert.Equal(t, runID, items[0].SourceRunID)
	assert.Contains(t, items[0].Content, "contract",
		"the recalled content is the classified error's recommendation")
}

// firstJSONLine returns the first non-empty line of a JSONL payload.
func firstJSONLine(t *testing.T, raw []byte) []byte {
	t.Helper()
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				return raw[start:i]
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		return raw[start:]
	}
	t.Fatalf("no JSON line in %q", string(raw))
	return nil
}

// The default index path must be the file the observer writes, on both sides.
// This is the regression guard for the mismatch described above: if either
// default moves, this fails instead of silently returning empty results.
func TestLessonIndexDefaultPathMatchesObserver(t *testing.T) {
	root := t.TempDir()
	observer := forgexruntime.NewFileObserver(forgexruntime.FileObserverConfig{
		Root:      root,
		AutoIndex: true,
	})

	t.Setenv(envLessonsFeed, "on")
	t.Setenv(envRuntimeRoot, root)
	t.Setenv(envIndexDB, "")
	t.Setenv(observerEnabledEnv, "on")
	t.Setenv(autoIndexEnv, "on")

	source := buildLessonSource()
	require.NotNil(t, source)
	defer func() {
		if closer, ok := source.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	// Drive one terminal run through the observer, then read through the
	// source: if the two sides picked different files, the recall is empty.
	seedFailedRun(t, observer, "wf-path-check")

	items, err := source.Recall(context.Background(), "contract", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, items,
		"the feed's default index path must be the file the observer's AutoIndex writes")
}

// seedFailedRun gives the observer a run directory that satisfies lessons.Derive's
// precondition (a halting stop decision comes from the terminal event; this adds
// the classified error it also requires), then drives the terminal event.
//
// It returns the run id. Shared by the chain tests above so the precondition
// lives in one place: Derive needs BOTH halves, and a test that supplies only
// one silently asserts nothing.
func seedFailedRun(t *testing.T, observer *forgexruntime.FileObserver, workflowID string) string {
	t.Helper()
	runID := forgexruntime.RunIDForWorkflow(workflowID)
	runDir := observer.Store().Layout().RunDir(runID)
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	envelope := model.ErrorEnvelope{
		ID:        "err-" + workflowID,
		RunID:     runID,
		Category:  "contract_violation",
		Severity:  "error",
		Message:   "the handler was called with a parameter the contract forbids",
		Operation: "shadow_validate",
		Timestamp: time.Now().UTC(),
		Metadata: map[string]string{
			"recommendation": "check the contract before dispatching the call",
		},
	}
	raw, err := json.Marshal(envelope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "errors.jsonl"), append(raw, '\n'), 0o644))

	require.NoError(t, observer.ObserveEvent(context.Background(), &forgestorage.Event{
		WorkflowID: workflowID,
		Type:       forgestorage.EventWorkflowFailed,
		Timestamp:  time.Now().UTC(),
	}))
	return runID
}

// With the feed on but neither writer switch set, the log says so — the
// "everything looks on, nothing works" case is made visible instead of silent.
func TestWarnWhenNothingWillIndex(t *testing.T) {
	t.Setenv(observerEnabledEnv, "")
	t.Setenv(autoIndexEnv, "")

	// Capture the log output for the warning.
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	warnIfNothingWillIndex("/tmp/index.db")

	out := buf.String()
	assert.Contains(t, out, observerEnabledEnv, "the warning names the observer switch")
	assert.Contains(t, out, autoIndexEnv, "the warning names the auto-index switch")
	assert.Contains(t, out, envLessonsFeed, "the warning names the switch the operator did set")

	// With both set, there is nothing to warn about.
	buf.Reset()
	t.Setenv(observerEnabledEnv, "on")
	t.Setenv(autoIndexEnv, "on")
	warnIfNothingWillIndex("/tmp/index.db")
	assert.Empty(t, buf.String(), "no warning when the writers are configured")
}
