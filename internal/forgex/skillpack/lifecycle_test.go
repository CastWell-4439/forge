package skillpack

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishedPack builds a minimal published pack for lifecycle tests.
func publishedPack(id string) Pack {
	return Pack{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			ID:           id,
			Version:      "0.1.0",
			Status:       StatusPublished,
			ReviewStatus: ReviewPending,
			GeneratedAt:  time.Now().UTC(),
		},
		Spec: Spec{
			Trigger:         Trigger{Description: "when the thing happens"},
			ToolPermissions: []string{"shell.run"},
		},
		Readme: "a written readme",
	}
}

// --- deprecation ---

// Deprecating retires a skill WITHOUT deleting it: the file stays, the reason
// is kept, and the decision can be undone.
func TestDeprecateKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)

	pack, err := store.Deprecate("sk-1", "the tool it wraps was removed", time.Now().UTC())
	require.NoError(t, err)

	assert.Equal(t, StatusDeprecated, pack.Metadata.Status)
	assert.Equal(t, "the tool it wraps was removed", pack.Metadata.DeprecationReason)
	assert.False(t, pack.Metadata.DeprecatedAt.IsZero())

	// The file is still there and still loads.
	_, err = os.Stat(filepath.Join(dir, "sk-1.yaml"))
	require.NoError(t, err, "deprecation must not delete the file")

	loaded, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.True(t, loaded.Metadata.IsDeprecated(), "and the status persists")
}

// A retirement must say why: whoever finds the skill later needs to know
// whether the world moved or the skill was wrong.
func TestDeprecateRequiresAReason(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)

	_, err = store.Deprecate("sk-1", "", time.Now().UTC())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason is required")

	_, err = store.Deprecate("sk-1", "   ", time.Now().UTC())
	require.Error(t, err, "whitespace is not a reason")

	// And the skill is untouched.
	pack, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.False(t, pack.Metadata.IsDeprecated())
}

// A draft was never in use, so there is nothing to retire: discarding it is a
// different act, and conflating the two would let "deprecated" quietly mean
// "I did not finish this".
func TestDeprecateRefusesADraft(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), true)
	require.NoError(t, err)

	_, err = store.Deprecate("sk-1", "not applicable", time.Now().UTC())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "draft")
}

// Re-deprecating keeps the FIRST time and reason: refreshing them on every run
// would erase when and why it actually happened.
func TestDeprecateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = store.Deprecate("sk-1", "the original reason", first)
	require.NoError(t, err)

	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pack, err := store.Deprecate("sk-1", "a second reason", later)
	require.NoError(t, err)

	assert.True(t, pack.Metadata.DeprecatedAt.Equal(first), "the retirement clock keeps its start")
	assert.Equal(t, "the original reason", pack.Metadata.DeprecationReason)
}

// --- restoration ---

// Restoring clears the deprecation fields rather than leaving them: a skill
// that is both published and carrying a retirement reason would make the next
// reader guess which field to believe.
func TestRestoreClearsDeprecation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)
	_, err = store.Deprecate("sk-1", "was wrong about this", time.Now().UTC())
	require.NoError(t, err)

	pack, err := store.Restore("sk-1", time.Now().UTC())
	require.NoError(t, err)

	assert.Equal(t, StatusPublished, pack.Metadata.Status)
	assert.False(t, pack.Metadata.IsDeprecated())
	assert.True(t, pack.Metadata.DeprecatedAt.IsZero(), "the retirement time is cleared")
	assert.Empty(t, pack.Metadata.DeprecationReason, "and so is the reason")

	// It reads back the same way.
	loaded, err := store.Load("sk-1")
	require.NoError(t, err)
	assert.Equal(t, StatusPublished, loaded.Metadata.Status)
	assert.Empty(t, loaded.Metadata.DeprecationReason)
}

func TestRestoreRefusesANonDeprecatedSkill(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)

	_, err = store.Restore("sk-1", time.Now().UTC())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not deprecated")
}

// --- review ---

// Marking a review stamps the TIME as well as the status: staleness is a
// question about when, and the status alone cannot answer it.
func TestMarkReviewedStampsTime(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pack, err := store.MarkReviewed("sk-1", at)
	require.NoError(t, err)

	assert.Equal(t, ReviewDone, pack.Metadata.ReviewStatus)
	assert.True(t, pack.Metadata.ReviewedAt.Equal(at))
}

// A deprecated skill is not reviewed: it is out of use, so confirming that it
// still applies is a contradiction. Restoring it first is the honest path.
func TestReviewRefusesADeprecatedSkill(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)
	_, err = store.Deprecate("sk-1", "no longer applies", time.Now().UTC())
	require.NoError(t, err)

	_, err = store.MarkReviewed("sk-1", time.Now().UTC())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore it first")
}

// --- listing ---

// Deprecated skills are hidden by default: "what skills do we have" almost
// always means "what can an agent use".
func TestListHidesDeprecated(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	_, err := store.Save(publishedPack("keep"), false)
	require.NoError(t, err)
	_, err = store.Save(publishedPack("retire"), false)
	require.NoError(t, err)
	_, err = store.Deprecate("retire", "no longer applies", time.Now().UTC())
	require.NoError(t, err)

	visible, err := store.List()
	require.NoError(t, err)
	require.Len(t, visible, 1)
	assert.Equal(t, "keep", visible[0].Metadata.ID)

	// The explicit way to see everything still works.
	all, err := store.ListAll()
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// --- staleness ---

// A reviewed skill whose confirmation has aged out is stale; a fresh one is
// not.
func TestStaleByReviewAge(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	window := 180 * 24 * time.Hour

	fresh := Metadata{ReviewStatus: ReviewDone, ReviewedAt: now.Add(-30 * 24 * time.Hour)}
	assert.False(t, fresh.IsStale(now, window))

	old := Metadata{ReviewStatus: ReviewDone, ReviewedAt: now.Add(-400 * 24 * time.Hour)}
	assert.True(t, old.IsStale(now, window))

	// Exactly at the boundary is not yet stale: the comparison is "longer than".
	boundary := Metadata{ReviewStatus: ReviewDone, ReviewedAt: now.Add(-window)}
	assert.False(t, boundary.IsStale(now, window))
}

// An unreviewed skill is not stale — it is unfinished, and the review's own
// rules already route drafts. Reporting it here would confuse neglect with
// incompleteness.
func TestUnreviewedIsNotStale(t *testing.T) {
	now := time.Now()
	pending := Metadata{ReviewStatus: ReviewPending, ReviewedAt: now.Add(-1000 * 24 * time.Hour)}
	assert.False(t, pending.IsStale(now, 180*24*time.Hour),
		"a pending review is unfinished, not neglected")
}

// A reviewed skill with no recorded time is UNKNOWN, not ancient. Reporting it
// would flag every skill written before the field existed.
func TestUnknownReviewTimeIsNotStale(t *testing.T) {
	reviewed := Metadata{ReviewStatus: ReviewDone}
	assert.False(t, reviewed.IsStale(time.Now(), 180*24*time.Hour))
}

// The window is configurable, and a non-positive one falls back to the default
// rather than making everything stale.
func TestStaleWindowIsConfigurable(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	reviewed := Metadata{ReviewStatus: ReviewDone, ReviewedAt: now.Add(-40 * 24 * time.Hour)}

	assert.False(t, reviewed.IsStale(now, 180*24*time.Hour))
	assert.True(t, reviewed.IsStale(now, 30*24*time.Hour), "a shorter window catches it")

	// A zero window uses the default, not "everything is stale".
	assert.False(t, reviewed.IsStale(now, 0))
	assert.Equal(t, 180*24*time.Hour, DefaultStaleAfter)
}

// StaleSkills lists them, and nothing changes status.
func TestStaleSkillsReports(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for _, id := range []string{"fresh", "old"} {
		_, err := store.Save(publishedPack(id), false)
		require.NoError(t, err)
	}
	_, err := store.MarkReviewed("fresh", now.Add(-10*24*time.Hour))
	require.NoError(t, err)
	_, err = store.MarkReviewed("old", now.Add(-400*24*time.Hour))
	require.NoError(t, err)

	stale, err := store.StaleSkills(now, 180*24*time.Hour)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "old", stale[0].Metadata.ID)

	// Reporting changed nothing.
	all, err := store.ListAll()
	require.NoError(t, err)
	for _, p := range all {
		assert.Equal(t, StatusPublished, p.Metadata.Status, "a report must not change status")
	}
}

// A deprecated skill is not reported as stale: it is already out of use, and
// asking someone to re-review it would be noise.
func TestDeprecatedIsNotReportedStale(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	_, err := store.Save(publishedPack("sk-1"), false)
	require.NoError(t, err)
	_, err = store.MarkReviewed("sk-1", now.Add(-400*24*time.Hour))
	require.NoError(t, err)
	_, err = store.Deprecate("sk-1", "retired", now)
	require.NoError(t, err)

	stale, err := store.StaleSkills(now, 180*24*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, stale, "a retired skill needs no review")
}

// --- backward compatibility ---

// A pack written before the lifecycle fields existed still loads, and reads as
// "never reviewed" rather than as "ancient".
func TestOldPackStillLoads(t *testing.T) {
	dir := t.TempDir()
	old := `apiVersion: forgex/v1
kind: SkillPack
metadata:
  id: legacy
  version: 0.1.0
  status: published
  review_status: reviewed
  generated_at: 2026-01-01T00:00:00Z
spec:
  trigger:
    description: an older skill
  steps: []
  tool_permissions: []
  eval:
    suite: ""
    cases: []
readme: a written readme
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.yaml"), []byte(old), 0o644))

	pack, err := NewStore(dir).Load("legacy")
	require.NoError(t, err)
	assert.Equal(t, "legacy", pack.Metadata.ID)
	assert.Equal(t, ReviewDone, pack.Metadata.ReviewStatus)
	assert.True(t, pack.Metadata.ReviewedAt.IsZero(), "the missing field reads as unknown")
	assert.False(t, pack.Metadata.IsDeprecated())
	assert.False(t, pack.Metadata.IsStale(time.Now(), 180*24*time.Hour),
		"an unknown review time is not evidence of neglect")
}
