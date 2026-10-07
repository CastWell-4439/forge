package storage

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
)

func indexWithLessons(t *testing.T) *SQLiteIndex {
	t.Helper()
	idx, err := OpenSQLiteIndex(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func lesson(id, runID, title, category, content string, at time.Time) model.Lesson {
	return model.Lesson{
		ID: id, SourceRunID: runID, Title: title, Category: category,
		Content: content, CreatedAt: at,
	}
}

// Lessons are indexed into the run's rows and readable ACROSS runs — the
// capability that did not exist before (only per-run LessonsFile lookup did).
func TestSearchLessonsAcrossRuns(t *testing.T) {
	idx := indexWithLessons(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.IndexArtifacts(ctx, indexArtifacts{
		Run:     model.Run{ID: "run-1", TaskID: "t1", Name: "n1", Status: model.RunFailed, StartedAt: now},
		Lessons: []model.Lesson{lesson("l1", "run-1", "Publish gate", "reliability", "the readme template blocks publish", now)},
	}))
	require.NoError(t, idx.IndexArtifacts(ctx, indexArtifacts{
		Run:     model.Run{ID: "run-2", TaskID: "t2", Name: "n2", Status: model.RunSucceeded, StartedAt: now.Add(time.Second)},
		Lessons: []model.Lesson{lesson("l2", "run-2", "Cache keys", "performance", "include the schema version in the cache key", now.Add(time.Second))},
	}))

	// Query across both runs, newest first.
	got, err := idx.SearchLessons(ctx, "cache", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "l2", got[0].ID)
	assert.Equal(t, "run-2", got[0].SourceRunID, "provenance survives the index")

	// Match on category as well as content.
	got, err = idx.SearchLessons(ctx, "reliability", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "l1", got[0].ID)

	// Case-insensitive.
	got, err = idx.SearchLessons(ctx, "PUBLISH", 10)
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// An empty query returns the newest lessons: "what have we learned lately"
	// is a legitimate question with no keywords.
	got, err = idx.SearchLessons(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "l2", got[0].ID, "newest first")
}

// Re-indexing the same run replaces its lessons rather than duplicating them.
func TestSearchLessonsReindexDoesNotDuplicate(t *testing.T) {
	idx := indexWithLessons(t)
	ctx := context.Background()
	now := time.Now().UTC()
	artifacts := indexArtifacts{
		Run:     model.Run{ID: "run-1", TaskID: "t", Name: "n", Status: model.RunFailed, StartedAt: now},
		Lessons: []model.Lesson{lesson("l1", "run-1", "Once", "cat", "body", now)},
	}

	require.NoError(t, idx.IndexArtifacts(ctx, artifacts))
	require.NoError(t, idx.IndexArtifacts(ctx, artifacts))

	got, err := idx.SearchLessons(ctx, "", 10)
	require.NoError(t, err)
	assert.Len(t, got, 1, "the second index pass replaced, not appended")
}

// A run with no lessons is the common case and must not error.
func TestSearchLessonsNoLessonsIsEmpty(t *testing.T) {
	idx := indexWithLessons(t)
	ctx := context.Background()
	require.NoError(t, idx.IndexArtifacts(ctx, indexArtifacts{
		Run: model.Run{ID: "run-1", TaskID: "t", Name: "n", Status: model.RunSucceeded, StartedAt: time.Now().UTC()},
	}))

	got, err := idx.SearchLessons(ctx, "anything", 5)
	require.NoError(t, err)
	assert.Empty(t, got, "an index with no lessons answers with nothing, not an error")
}

// Every run directory gets the lessons file read on index (loaded from disk,
// not passed in memory) — the path the observer actually writes to.
func TestIndexRunDirLoadsLessons(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	runJSON, err := json.Marshal(model.Run{
		ID: "run-dir", TaskID: "t", Name: "n", Status: model.RunFailed, StartedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.json"), runJSON, 0o644))

	lessonJSON, err := json.Marshal(lesson("l-dir", "run-dir", "From disk", "category", "loaded off the run directory", now))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lessons.jsonl"), append(lessonJSON, '\n'), 0o644))

	idx := indexWithLessons(t)
	require.NoError(t, idx.IndexRunDir(context.Background(), dir))

	got, err := idx.SearchLessons(context.Background(), "disk", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "l-dir", got[0].ID)
}
