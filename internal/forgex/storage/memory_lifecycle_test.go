package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/forgex/model"
)

// openTestIndex creates a fresh index in a temp directory.
func openTestIndex(t *testing.T) *SQLiteIndex {
	t.Helper()
	idx, err := OpenSQLiteIndex(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.Init(context.Background()))
	return idx
}

// --- observations ---

func TestRecordAndLoadObservations(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory,
		Used: false, UsageKnown: true, At: now,
	}))

	obs, err := idx.LoadObservations(ctx, ObservationKindMemory, 0)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	assert.Equal(t, "run-1", obs[0].RunID)
	assert.Equal(t, "e1", obs[0].EntryID)
	assert.True(t, obs[0].UsageKnown)
	assert.False(t, obs[0].Used)
}

// A run's view of an entry is ONE row: a run that recalls the same entry in ten
// steps must not look like ten independent confirmations of disuse.
func TestObservationIsOneRowPerRunAndEntry(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
			RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory,
			Used: false, UsageKnown: true, At: now.Add(time.Duration(i) * time.Second),
		}))
	}

	obs, err := idx.LoadObservations(ctx, ObservationKindMemory, 0)
	require.NoError(t, err)
	assert.Len(t, obs, 1, "a repeated recall within one run is still one observation")
}

// A later observation that reports use upgrades the run's row: "used at least
// once in this run" is the fact worth recording.
func TestObservationUseUpgrades(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory, Used: false, UsageKnown: false, At: now,
	}))
	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory, Used: true, UsageKnown: true, At: now.Add(time.Second),
	}))

	obs, err := idx.LoadObservations(ctx, ObservationKindMemory, 0)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	assert.True(t, obs[0].Used, "a later use is not overwritten by an earlier silence")
	assert.True(t, obs[0].UsageKnown)
}

// A downgrade is not possible: once a run reported a signal, a later recall
// without one must not erase it.
func TestObservationSignalIsNotDowngraded(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory, Used: true, UsageKnown: true, At: now,
	}))
	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "run-1", EntryID: "e1", Kind: ObservationKindMemory, Used: false, UsageKnown: false, At: now.Add(time.Second),
	}))

	obs, err := idx.LoadObservations(ctx, ObservationKindMemory, 0)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	assert.True(t, obs[0].UsageKnown, "the known signal survives")
	assert.True(t, obs[0].Used)
}

// Observations are separated by kind, so archiving memories cannot be confused
// by lesson evidence.
func TestObservationsFilteredByKind(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "r1", EntryID: "mem-1", Kind: ObservationKindMemory, UsageKnown: true, At: now,
	}))
	require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
		RunID: "r1", EntryID: "les-1", Kind: ObservationKindLesson, UsageKnown: true, At: now,
	}))

	mem, err := idx.LoadObservations(ctx, ObservationKindMemory, 0)
	require.NoError(t, err)
	assert.Len(t, mem, 1)
	assert.Equal(t, "mem-1", mem[0].EntryID)

	les, err := idx.LoadObservations(ctx, ObservationKindLesson, 0)
	require.NoError(t, err)
	assert.Len(t, les, 1)
	assert.Equal(t, "les-1", les[0].EntryID)

	all, err := idx.LoadObservations(ctx, "", 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// An observation without a run or entry is refused: it could not be aggregated.
func TestObservationRequiresIDs(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()

	assert.Error(t, idx.RecordObservation(ctx, MemoryObservation{EntryID: "e1"}))
	assert.Error(t, idx.RecordObservation(ctx, MemoryObservation{RunID: "r1"}))
	assert.NoError(t, idx.RecordObservation(ctx, MemoryObservation{RunID: "r1", EntryID: "e1"}))
}

func TestCountObservations(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		require.NoError(t, idx.RecordObservation(ctx, MemoryObservation{
			RunID: "r" + string(rune('a'+i)), EntryID: "e" + string(rune('a'+i)),
			Kind: ObservationKindMemory, At: now,
		}))
	}
	n, err := idx.CountObservations(ctx, ObservationKindMemory)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// --- archive state ---

func TestArchiveAndList(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, idx.ArchiveEntry(ctx, "e1", ObservationKindMemory, "unused in 6 recalls", now))

	records, err := idx.ListArchive(ctx, ObservationKindMemory, false)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "e1", records[0].EntryID)
	assert.Equal(t, "unused in 6 recalls", records[0].Reason, "the reason is preserved for review")
	assert.False(t, records[0].Recovered)
}

// Re-archiving keeps the FIRST archive time: the retention period runs from when
// the entry left the pool, and refreshing it on every run would mean nothing is
// ever old enough to delete.
func TestReArchiveKeepsFirstTime(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, idx.ArchiveEntry(ctx, "e1", ObservationKindMemory, "first", first))
	require.NoError(t, idx.ArchiveEntry(ctx, "e1", ObservationKindMemory, "second", later))

	records, err := idx.ListArchive(ctx, ObservationKindMemory, false)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.True(t, records[0].ArchivedAt.Equal(first), "the retention clock starts at the first archive")
	assert.Equal(t, "second", records[0].Reason, "but the latest reason is kept")
}

// Recovery returns an entry to the pool, and the row is KEPT: a memory that
// keeps being archived and recovered is telling you the threshold is wrong, and
// deleting the evidence would hide that.
func TestRecoverKeepsHistory(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()

	require.NoError(t, idx.ArchiveEntry(ctx, "e1", ObservationKindMemory, "unused", time.Now().UTC()))
	require.NoError(t, idx.RecoverEntry(ctx, "e1", time.Now().UTC()))

	current, err := idx.ArchivedEntries(ctx, ObservationKindMemory)
	require.NoError(t, err)
	assert.NotContains(t, current, "e1", "a recovered entry is back in the pool")

	withHistory, err := idx.ListArchive(ctx, ObservationKindMemory, true)
	require.NoError(t, err)
	require.Len(t, withHistory, 1)
	assert.True(t, withHistory[0].Recovered, "the history survives")

	// And the default listing hides it, because the default question is "what
	// is out of the pool right now".
	currentOnly, err := idx.ListArchive(ctx, ObservationKindMemory, false)
	require.NoError(t, err)
	assert.Empty(t, currentOnly)
}

// Recovering something that is not archived is an error, not a silent no-op: an
// operator asking to recover a typo'd id should be told.
func TestRecoverUnarchivedIsAnError(t *testing.T) {
	idx := openTestIndex(t)
	err := idx.RecoverEntry(context.Background(), "never-archived", time.Now().UTC())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not archived")
}

// --- deletion ---

// The archive row is forgotten with the entry, so a later listing does not
// report a record that no longer exists.
func TestForgetEntry(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()

	require.NoError(t, idx.ArchiveEntry(ctx, "e1", ObservationKindMemory, "unused", time.Now().UTC()))
	require.NoError(t, idx.ForgetEntry(ctx, "e1"))

	records, err := idx.ListArchive(ctx, ObservationKindMemory, true)
	require.NoError(t, err)
	assert.Empty(t, records)
}

// Lessons are deleted directly: they live in this index, which is why the
// control plane can remove them while the agent only ever read them.
func TestDeleteLesson(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()

	require.NoError(t, idx.IndexArtifacts(ctx, indexArtifacts{
		Run: model.Run{ID: "run-1", TaskID: "t1", Name: "n", Status: model.RunSucceeded},
		Lessons: []model.Lesson{{
			ID: "lesson-1", Title: "a lesson", Category: "general", Content: "content",
		}},
	}))

	before, err := idx.SearchLessons(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, before, 1)

	require.NoError(t, idx.DeleteLesson(ctx, "lesson-1"))

	after, err := idx.SearchLessons(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, after)
}

func TestDeleteLessonRequiresID(t *testing.T) {
	idx := openTestIndex(t)
	assert.Error(t, idx.DeleteLesson(context.Background(), ""))
}

// --- closed index safety ---

// Every method refuses a closed index rather than panicking: a nil dereference
// in an offline tool is still a crash, and the tool has a report to print.
func TestClosedIndexIsSafe(t *testing.T) {
	idx, err := OpenSQLiteIndex(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	require.NoError(t, idx.Init(context.Background()))
	require.NoError(t, idx.Close())

	ctx := context.Background()
	assert.Error(t, idx.RecordObservation(ctx, MemoryObservation{RunID: "r", EntryID: "e"}))
	_, err = idx.LoadObservations(ctx, "", 0)
	assert.Error(t, err)
	assert.Error(t, idx.ArchiveEntry(ctx, "e", ObservationKindMemory, "", time.Now()))
	assert.Error(t, idx.RecoverEntry(ctx, "e", time.Now()))
	_, err = idx.ArchivedEntries(ctx, "")
	assert.Error(t, err)
	_, err = idx.ListArchive(ctx, "", false)
	assert.Error(t, err)
	assert.Error(t, idx.ForgetEntry(ctx, "e"))
	assert.Error(t, idx.DeleteLesson(ctx, "e"))
	_, err = idx.CountObservations(ctx, "")
	assert.Error(t, err)
}
