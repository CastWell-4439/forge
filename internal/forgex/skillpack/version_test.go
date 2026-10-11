package skillpack

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/forgex/model"
)

// src builds a minimal source case.
func src(id, description, suite string, tools []string, lessons []model.Lesson) *Source {
	s := &Source{
		RunID:     "run-" + id,
		ToolCalls: make([]model.ToolCall, 0, len(tools)),
		Lessons:   lessons,
	}
	s.Spec.ID = id
	s.Spec.Description = description
	s.Spec.Suite = suite
	for _, t := range tools {
		s.ToolCalls = append(s.ToolCalls, model.ToolCall{ToolName: t})
	}
	return s
}

// --- the version is derived, not remembered ---

// The version describes the content, so re-distilling an unchanged case gives
// the same one. A version that moved on every run would say nothing.
func TestVersionIsStableForUnchangedContent(t *testing.T) {
	s := src("case-a", "do the thing", "suite-1", []string{"file.read"}, nil)

	now := time.Now()
	first := Distill(s, now)
	second := Distill(s, now.Add(72*time.Hour)) // later, same content

	assert.Equal(t, first.Metadata.Version, second.Metadata.Version,
		"a later run over the same case is the same skill")
	assert.Contains(t, first.Metadata.Version, "+", "the version carries a content digest")
}

// A changed case yields a new version, which is what makes staleness visible
// without anyone remembering to bump a number.
func TestVersionChangesWhenContentChanges(t *testing.T) {
	now := time.Now()
	before := Distill(src("case-a", "do the thing", "suite-1",
		[]string{"file.read", "shell.run"}, nil), now)

	after := Distill(src("case-a", "do the thing", "suite-1",
		[]string{"file.read", "shell.run", "web.fetch"}, nil), now)

	assert.NotEqual(t, before.Metadata.Version, after.Metadata.Version,
		"a new tool is a different skill")
}

// The digest covers what the pack IS, not its lifecycle. Otherwise publishing
// or reviewing would change the version and it would stop describing content.
func TestVersionIgnoresLifecycleState(t *testing.T) {
	s := src("case-a", "do the thing", "suite-1", []string{"file.read"}, nil)
	base := Distill(s, time.Now())

	published := base
	published.Metadata.Status = StatusPublished
	published.Metadata.ReviewStatus = ReviewDone
	published.Metadata.ReviewedAt = base.Metadata.GeneratedAt.Add(50 * time.Hour)
	published.Metadata.GeneratedAt = base.Metadata.GeneratedAt.Add(100 * time.Hour)

	assert.Equal(t, base.Metadata.Version, published.Metadata.Version,
		"publishing a skill does not make it a different skill")

	// The id names the pack; it is not part of what the pack says.
	renamed := base
	renamed.Metadata.ID = "other-id"
	assert.Equal(t, ContentDigest(base), ContentDigest(renamed))
}

// The human's channel survives: a caller can put a pack on a semantic line.
func TestBaseVersionIsHonoured(t *testing.T) {
	s := src("case-a", "x", "suite-1", []string{"file.read"}, nil)

	pack, err := DistillMany([]*Source{s}, MergeSpec{ID: "merged", Base: "2.0.0"}, time.Now())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(pack.Metadata.Version, "2.0.0+"), "got %q", pack.Metadata.Version)

	dflt := Distill(s, time.Now())
	assert.True(t, strings.HasPrefix(dflt.Metadata.Version, DefaultBaseVersion+"+"),
		"the default base applies when none is given")
}

// --- merging several cases into one skill ---

// The gap this closes: distillation used to take exactly one case, so a skill
// built from several could not be expressed at all.
func TestDistillManyMergesSeveralCases(t *testing.T) {
	a := src("case-a", "first", "suite-1", []string{"file.read"}, nil)
	b := src("case-b", "second", "suite-1", []string{"shell.run"}, nil)

	pack, err := DistillMany([]*Source{a, b}, MergeSpec{ID: "merged-skill"}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, "merged-skill", pack.Metadata.ID)
	assert.Equal(t, []string{"case-a", "case-b"}, pack.Metadata.SourceCases,
		"the schema already had a list; now it is filled")
	assert.Equal(t, []string{"case-a", "case-b"}, pack.Spec.Eval.Cases)

	// Steps and permissions are unioned: the merged skill must be able to do
	// what either case did.
	names := stepNames(pack)
	assert.ElementsMatch(t, []string{"file.read", "shell.run"}, names)
	assert.ElementsMatch(t, []string{"file.read", "shell.run"}, pack.Spec.ToolPermissions)
}

// The merge is deterministic: source order must not change the result, or the
// version would churn and stop describing content.
func TestMergeIsOrderIndependent(t *testing.T) {
	a := src("case-a", "first", "suite-1", []string{"file.read", "shell.run"}, nil)
	b := src("case-b", "second", "suite-1", []string{"web.fetch"}, nil)
	now := time.Now()

	one, err := DistillMany([]*Source{a, b}, MergeSpec{ID: "m"}, now)
	require.NoError(t, err)
	two, err := DistillMany([]*Source{b, a}, MergeSpec{ID: "m"}, now)
	require.NoError(t, err)

	assert.Equal(t, one.Metadata.Version, two.Metadata.Version,
		"the same set of cases in a different order is the same skill")
	assert.Equal(t, ContentDigest(one), ContentDigest(two))
}

// Constraints are unioned, never intersected: a constraint is a prohibition, so
// dropping one to make two cases agree would be discarding safety to tidy a
// document.
func TestMergeUnionsConstraints(t *testing.T) {
	a := src("case-a", "first", "s", []string{"file.read"},
		[]model.Lesson{{ID: "l1", Title: "never delete without a backup", Content: "because it is irreversible"}})
	b := src("case-b", "second", "s", []string{"file.read"},
		[]model.Lesson{{ID: "l2", Title: "never push to protected branches", Content: "because reviews are required"}})

	pack, err := DistillMany([]*Source{a, b}, MergeSpec{ID: "m"}, time.Now())
	require.NoError(t, err)

	require.Len(t, pack.Spec.Constraints, 2, "both prohibitions survive the merge")
	rules := constraintRules(pack)
	assert.Contains(t, rules, "never delete without a backup")
	assert.Contains(t, rules, "never push to protected branches")
	assert.ElementsMatch(t, []string{"l1", "l2"}, pack.Metadata.SourceLessons)
}

// The same rule from two cases is one constraint, with both pieces of evidence
// recorded rather than one being lost.
func TestMergeDeduplicatesSharedConstraints(t *testing.T) {
	shared := func(id string) model.Lesson {
		return model.Lesson{ID: id, Title: "never delete without a backup", Content: "irreversible"}
	}
	a := src("case-a", "first", "s", []string{"file.read"}, []model.Lesson{shared("l1")})
	b := src("case-b", "second", "s", []string{"file.read"}, []model.Lesson{shared("l2")})

	pack, err := DistillMany([]*Source{a, b}, MergeSpec{ID: "m"}, time.Now())
	require.NoError(t, err)

	require.Len(t, pack.Spec.Constraints, 1, "one rule, not two")
	assert.Equal(t, "l1,l2", pack.Spec.Constraints[0].Evidence,
		"both cases are on record as evidence for it")
}

// A single source still goes down the original path, so an existing draft is
// unchanged apart from the version.
func TestSingleSourceKeepsTheOriginalShape(t *testing.T) {
	s := src("case-a", "the thing", "suite-1", []string{"a", "b", "a"}, nil)

	single := Distill(s, time.Now())
	merged, err := DistillMany([]*Source{s}, MergeSpec{ID: "case-a"}, time.Now())
	require.NoError(t, err)

	assert.Equal(t, ContentDigest(single), ContentDigest(merged),
		"one source is the single-case path, not the merge path")
	assert.Equal(t, []string{"case-a"}, single.Metadata.SourceCases)
	assert.Len(t, single.Spec.Steps, 2, "tools are still deduplicated in first-seen order")
}

// A merged pack needs a name: it is none of its sources, so the id is a
// decision rather than something to derive.
func TestMergedPackRequiresAnID(t *testing.T) {
	a := src("case-a", "x", "s", []string{"file.read"}, nil)
	b := src("case-b", "y", "s", []string{"shell.run"}, nil)

	_, err := DistillMany([]*Source{a, b}, MergeSpec{}, time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

// No sources is an error, not an empty pack: a skill built from nothing would
// look enforceable and enforce nothing.
func TestDistillManyRejectsNoSources(t *testing.T) {
	_, err := DistillMany(nil, MergeSpec{ID: "m"}, time.Now())
	require.Error(t, err)

	_, err = DistillMany([]*Source{nil, nil}, MergeSpec{ID: "m"}, time.Now())
	require.Error(t, err)
}

// A nil source is a programming error, not a crash.
func TestDistillNilSource(t *testing.T) {
	assert.NotPanics(t, func() { _ = Distill(nil, time.Now()) })
}

// --- replacing a version keeps the old one ---

// Saving a changed pack used to overwrite the previous version with no trace.
func TestSaveKeepsTheReplacedVersion(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now()

	first := Distill(src("pack-a", "one", "s", []string{"file.read"}, nil), now)
	_, err := store.Save(first, false)
	require.NoError(t, err)

	second := Distill(src("pack-a", "one", "s", []string{"file.read", "shell.run"}, nil), now)
	require.NotEqual(t, first.Metadata.Version, second.Metadata.Version,
		"precondition: the two saves are different revisions")
	_, err = store.Save(second, false)
	require.NoError(t, err)

	history, err := store.History("pack-a")
	require.NoError(t, err)
	require.Len(t, history, 1, "the replaced revision is kept")

	// The live pack is the new one.
	live, err := store.Load("pack-a")
	require.NoError(t, err)
	assert.Equal(t, second.Metadata.Version, live.Metadata.Version)
}

// Re-saving the same content is not a new revision, so it does not fill the
// history with duplicates of one document.
func TestSaveDoesNotArchiveAnIdenticalVersion(t *testing.T) {
	store := NewStore(t.TempDir())
	pack := Distill(src("pack-b", "one", "s", []string{"file.read"}, nil), time.Now())

	for i := 0; i < 3; i++ {
		_, err := store.Save(pack, false)
		require.NoError(t, err)
	}

	history, err := store.History("pack-b")
	require.NoError(t, err)
	assert.Empty(t, history, "the same revision saved three times is one revision")
}

// The archive holds one copy per distinct replaced revision: re-archiving a
// version already recorded neither duplicates it nor overwrites the first copy,
// which is the one that says what that revision actually was.
func TestArchiveKeepsOneCopyPerRevision(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now()

	original := Distill(src("pack-c", "one", "s", []string{"file.read"}, nil), now)
	changed := Distill(src("pack-c", "two", "s", []string{"file.read", "shell.run"}, nil), now)
	require.NotEqual(t, original.Metadata.Version, changed.Metadata.Version)

	_, err := store.Save(original, false)
	require.NoError(t, err)
	_, err = store.Save(changed, false)
	require.NoError(t, err)
	// Roll back to the original: the changed revision is now the one replaced.
	_, err = store.Save(original, false)
	require.NoError(t, err)
	// And forward again, which offers the original for archiving a second time.
	_, err = store.Save(changed, false)
	require.NoError(t, err)

	history, err := store.History("pack-c")
	require.NoError(t, err)
	assert.Len(t, history, 2, "two distinct revisions were replaced; each is kept once")
}

// A store with no history has an empty one, not an error.
func TestHistoryOfAnUnchangedPackIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())

	history, err := store.History("never-saved")
	require.NoError(t, err)
	assert.Empty(t, history)
}

// The history directory is hidden from the pack listing, so archived revisions
// do not appear as published skills.
func TestHistoryIsNotListedAsAPack(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now()

	_, err := store.Save(Distill(src("pack-d", "one", "s", []string{"file.read"}, nil), now), false)
	require.NoError(t, err)
	_, err = store.Save(Distill(src("pack-d", "two", "s", []string{"file.read"}, nil), now), false)
	require.NoError(t, err)

	packs, err := store.List()
	require.NoError(t, err)
	require.Len(t, packs, 1, "one published pack, not its archived revision too")
	assert.Equal(t, "pack-d", packs[0].Metadata.ID)

	entries, err := filepath.Glob(filepath.Join(store.Root, "*.yaml"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the layout is unchanged: one file per id in the root")
}

// --- helpers ---

func stepNames(p Pack) []string {
	out := make([]string, 0, len(p.Spec.Steps))
	for _, s := range p.Spec.Steps {
		out = append(out, s.Name)
	}
	return out
}

func constraintRules(p Pack) []string {
	out := make([]string, 0, len(p.Spec.Constraints))
	for _, c := range p.Spec.Constraints {
		out = append(out, c.DoNot)
	}
	return out
}
