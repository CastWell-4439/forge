package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcore "github.com/castwell/forge/internal/agent/core"
)

// writeStore creates a knowledge directory with the given documents.
//
// The shape mirrors the agent plane's on-disk format exactly, because the
// projector's whole job is to know that shape; a test that wrote a different
// one would pass while the real store failed to load.
func writeStore(t *testing.T, docs []map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(docs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, documentsFileName), data, 0o644))
	return dir
}

// memoryDoc builds one stored document in the agent plane's shape.
func memoryDoc(id, content, category string, meta map[string]string) map[string]any {
	merged := map[string]string{"category": category}
	for k, v := range meta {
		merged[k] = v
	}
	return map[string]any{
		"doc": map[string]any{"id": id, "content": content, "metadata": merged},
	}
}

// The export reads memories with their governance metadata intact.
func TestExportReadsMemories(t *testing.T) {
	observed := "2026-03-01T12:00:00Z"
	dir := writeStore(t, []map[string]any{
		memoryDoc("mem-1", "the project is written in go", "experience", map[string]string{
			"source":      "run:abc",
			"confidence":  "0.8",
			"observed_at": observed,
			"created_at":  observed,
			"layer":       "fact",
		}),
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)
	require.Len(t, export.Memories, 1)

	m := export.Memories[0]
	assert.Equal(t, "mem-1", m.ID)
	assert.Equal(t, "the project is written in go", m.Content)
	assert.Equal(t, "run:abc", m.Source)
	assert.InDelta(t, 0.8, m.Confidence, 1e-9)
	assert.Equal(t, agentcore.LayerFact, m.Layer)
	assert.Equal(t, 2026, m.ObservedAt.Year())
}

// Knowledge-base documents are not memories: the store holds both, and putting
// reference material into a lifecycle review would be wrong.
func TestExportSkipsNonMemories(t *testing.T) {
	dir := writeStore(t, []map[string]any{
		memoryDoc("mem-1", "a memory", "experience", nil),
		memoryDoc("doc-1", "a reference document", "knowledge", nil),
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)
	assert.Len(t, export.Memories, 1, "only the memory")
	assert.Equal(t, 1, export.Skipped, "and the reference document is counted, not lost")
}

// A document with no category is not a memory either: the memory plane always
// writes one, so its absence means the document came from somewhere else.
func TestExportSkipsUncategorised(t *testing.T) {
	dir := writeStore(t, []map[string]any{
		{"doc": map[string]any{"id": "x", "content": "no category"}},
		{"doc": map[string]any{"id": "", "content": "no id"}},
		{"doc": map[string]any{"id": "y", "content": ""}},
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)
	assert.Empty(t, export.Memories)
	assert.Equal(t, 3, export.Skipped)
}

// A store written before the governance fields existed still exports: the
// metadata is absent, and the memory reads as an unlabelled observation.
func TestExportOldStore(t *testing.T) {
	dir := writeStore(t, []map[string]any{
		memoryDoc("old-1", "an older memory", "experience", map[string]string{
			"created_at": "2026-01-01T00:00:00Z",
		}),
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)
	require.Len(t, export.Memories, 1)

	m := export.Memories[0]
	assert.Empty(t, m.Source)
	assert.Zero(t, m.Confidence)
	assert.Equal(t, agentcore.LayerEpisodic, m.Layer,
		"an unlabelled memory is an observation, not a claim")
}

// A missing store is not an error: a deployment that never wrote a memory is
// not a failure, and the report says "no memories" rather than "could not read".
func TestExportMissingStore(t *testing.T) {
	export, err := LoadMemoryExport(filepath.Join(t.TempDir(), "nonexistent"))
	require.NoError(t, err)
	assert.Empty(t, export.Memories)
	assert.Contains(t, export.Path, documentsFileName, "the path is still reported")
}

// A corrupt store is an error, unlike a missing one: the difference between
// "nothing to read" and "something is broken" matters to an operator.
func TestExportCorruptStore(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, documentsFileName), []byte("not json"), 0o644))

	_, err := LoadMemoryExport(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse memory store")
}

// An empty path falls back to the default directory, so the command works when
// run beside a worker without configuration.
func TestExportDefaultsDirectory(t *testing.T) {
	export, err := LoadMemoryExport("")
	require.NoError(t, err)
	// Compared through filepath.Join rather than against the constant: the
	// separator is platform-specific, and a hard-coded slash fails on Windows
	// while telling us nothing about the code.
	assert.Contains(t, export.Path, filepath.Join(DefaultKnowledgeDir, documentsFileName))
}

// --- claims projection ---

// The export turns into verification input without a separate step, which is
// what connects the review to the verification.
func TestClaimsFromExport(t *testing.T) {
	dir := writeStore(t, []map[string]any{
		memoryDoc("mem-1", "the project is written in go", "experience", nil),
		memoryDoc("mem-2", "production runs on v2.1", "experience", nil),
		memoryDoc("mem-3", "the run went fine", "experience", nil), // no claim
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)

	claims := ClaimsFromExport(export)
	require.Len(t, claims, 2, "the claimless memory produces no entry")
	assert.Contains(t, claims, "mem-1")
	assert.Contains(t, claims, "mem-2")

	var kinds []string
	for _, a := range claims["mem-1"] {
		kinds = append(kinds, a.Kind)
	}
	assert.Contains(t, kinds, "language", "the extractor found the language claim")
}

// --- ordering ---

// Newest first, then by id, so two exports of one store agree.
func TestExportSortsNewestFirst(t *testing.T) {
	dir := writeStore(t, []map[string]any{
		memoryDoc("b", "older", "experience", map[string]string{"observed_at": "2026-01-01T00:00:00Z"}),
		memoryDoc("a", "newer", "experience", map[string]string{"observed_at": "2026-06-01T00:00:00Z"}),
		memoryDoc("c", "newest", "experience", map[string]string{"observed_at": "2026-09-01T00:00:00Z"}),
	})

	export, err := LoadMemoryExport(dir)
	require.NoError(t, err)

	sorted := export.SortedMemories()
	require.Len(t, sorted, 3)
	assert.Equal(t, "c", sorted[0].ID)
	assert.Equal(t, "a", sorted[1].ID)
	assert.Equal(t, "b", sorted[2].ID)

	// The input is not reordered underneath the caller.
	assert.Equal(t, "b", export.Memories[0].ID)
}

// A memory with no observation time falls back to its write time, so it does
// not sort as infinitely old.
func TestObservedAtFallsBackToCreated(t *testing.T) {
	created := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	m := agentcore.ExportedMemory{CreatedAt: created}
	assert.Equal(t, created, m.ObservedAtOrCreated())
}
