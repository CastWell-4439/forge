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

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/storage"
)

// FORGE_LESSONS_FEED: off unless explicitly on — a cross-plane feed is opt-in,
// and a typo must not start feeding the agent lessons.
func TestLessonsEnabledDefaultsOff(t *testing.T) {
	t.Setenv(envLessonsFeed, "")
	assert.False(t, lessonsEnabled(), "default is off")

	for _, on := range []string{"on", "1", "true", "yes", "ON"} {
		t.Setenv(envLessonsFeed, on)
		assert.True(t, lessonsEnabled(), "%q enables the feed", on)
	}
	for _, off := range []string{"off", "0", "false", "no"} {
		t.Setenv(envLessonsFeed, off)
		assert.False(t, lessonsEnabled(), "%q keeps it off", off)
	}
	t.Setenv(envLessonsFeed, "onn") // typo
	assert.False(t, lessonsEnabled(), "a typo must not silently enable a new feed")
}

// The mapping is the contract between planes: every field the agent needs
// survives, and nothing forgex-specific leaks into the agent type.
func TestRecallItemFromLessonCarriesProvenance(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	item := recallItemFromLesson(model.Lesson{
		ID: "l1", SourceRunID: "run-9", Title: "T", Category: "C",
		Content: "body", CreatedAt: now,
	})

	assert.Equal(t, "l1", item.ID)
	assert.Equal(t, "T", item.Title)
	assert.Equal(t, "C", item.Category)
	assert.Equal(t, "body", item.Content)
	assert.Equal(t, "run-9", item.SourceRunID, "provenance must survive the plane crossing")
	assert.Equal(t, now, item.CreatedAt)
}

// End to end across the real boundary: a lesson written by the control plane's
// store is recallable through the agent-side interface, with no agent import of
// forgex anywhere in the path.
func TestLessonSourceReadsRealIndex(t *testing.T) {
	dir := t.TempDir()
	idx, err := storage.OpenSQLiteIndex(filepath.Join(dir, "index.db"))
	require.NoError(t, err)
	defer idx.Close()

	// Write a run directory the way the observer does, then index it: the path
	// a real lesson travels.
	now := time.Now().UTC()
	runDir := filepath.Join(dir, "run-7")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	runJSON, err := json.Marshal(model.Run{
		ID: "run-7", TaskID: "t", Name: "n", Status: model.RunFailed, StartedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "run.json"), runJSON, 0o644))
	lessonJSON, err := json.Marshal(model.Lesson{
		ID: "l1", SourceRunID: "run-7", Title: "Gate", Category: "reliability",
		Content: "the publish gate blocks a template readme", CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "lessons.jsonl"), append(lessonJSON, '\n'), 0o644))
	require.NoError(t, idx.IndexRunDir(context.Background(), runDir))

	source := &forgexLessons{index: idx}
	items, err := source.Recall(context.Background(), "publish", 3)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "l1", items[0].ID)
	assert.Equal(t, "run-7", items[0].SourceRunID)
	assert.Contains(t, items[0].Content, "publish gate")

	// A query matching nothing is an empty result, not an error.
	items, err = source.Recall(context.Background(), "nothing-matches-this", 3)
	require.NoError(t, err)
	assert.Empty(t, items)
}

// The channel is off by default, so assembly returns no source at all and the
// agent keeps its historical behaviour.
func TestBuildLessonSourceNilWhenDisabled(t *testing.T) {
	t.Setenv(envLessonsFeed, "off")
	assert.Nil(t, buildLessonSource(), "disabled means no source, not an empty one")

	// Enabled with a fresh path: the index is created and usable (a first run
	// has no lessons yet, which is normal).
	t.Setenv(envLessonsFeed, "on")
	t.Setenv(envIndexDB, filepath.Join(t.TempDir(), "fresh.db"))
	source := buildLessonSource()
	require.NotNil(t, source)
	items, err := source.Recall(context.Background(), "anything", 3)
	require.NoError(t, err, "an empty index answers, it does not fail")
	assert.Empty(t, items)

	// The source owns the handle it opened: closing it must release the
	// database file (a leak here is a real defect, not a test artefact).
	closer, ok := source.(interface{ Close() error })
	require.True(t, ok, "a source that opens a connection must be able to close it")
	require.NoError(t, closer.Close())
}

// An unreadable index degrades to "no lessons" instead of failing the
// registration of the worker (F3-8).
func TestBuildLessonSourceDegradesOnBadIndex(t *testing.T) {
	t.Setenv(envLessonsFeed, "on")
	// A directory where the database file should be: opening it must fail.
	t.Setenv(envIndexDB, t.TempDir())
	assert.Nil(t, buildLessonSource(), "an unusable index degrades to no lessons")
}
