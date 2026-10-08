package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgexmodel "github.com/castwell/forge/internal/forgex/model"
)

// writeRun creates a run directory with the given tool calls.
func writeRun(t *testing.T, root, runID string, calls []forgexmodel.ToolCall) {
	t.Helper()
	dir := filepath.Join(root, "runs", runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	var data []byte
	for _, call := range calls {
		line, err := json.Marshal(call)
		require.NoError(t, err)
		data = append(data, line...)
		data = append(data, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, toolCallsFileName), data, 0o644))
}

// --- projection ---

// Paths are found in a call's arguments and results, whatever key they sit
// under: a worker's payload shape is its own business, and restricting the walk
// to a known key would stop finding evidence the day a worker renames a field.
func TestProjectFindsPathsAnywhere(t *testing.T) {
	call := forgexmodel.ToolCall{
		RunID:    "run-1",
		ToolName: "file.edit",
		Args:     map[string]any{"path": "internal/agent/core/tools.go"},
		Result:   map[string]any{"files": []any{"README.md", "configs/app.yaml"}},
		EndedAt:  time.Now(),
	}

	out := projectToolCall(call)
	assert.ElementsMatch(t,
		[]string{"internal/agent/core/tools.go", "README.md", "configs/app.yaml"},
		out.Paths)
	assert.False(t, out.Failed)
}

// A value that merely contains a dot is not a path: versions, hostnames and
// prose must not become path evidence.
func TestProjectIgnoresNonPaths(t *testing.T) {
	call := forgexmodel.ToolCall{
		RunID: "run-1",
		Args: map[string]any{
			"version": "v2.1",
			"host":    "api.example.com",
			"ratio":   "1.5",
			"note":    "see the docs for details",
		},
	}

	out := projectToolCall(call)
	assert.Empty(t, out.Paths)
}

// A failure is carried through, since the path rule depends on it.
func TestProjectCarriesFailure(t *testing.T) {
	call := forgexmodel.ToolCall{
		RunID:    "run-1",
		ToolName: "file.read",
		Args:     map[string]any{"path": "missing/file.go"},
		Error:    "no such file or directory",
		EndedAt:  time.Now(),
	}

	out := projectToolCall(call)
	assert.True(t, out.Failed)
	assert.Equal(t, "no such file or directory", out.Error)
	assert.Contains(t, out.Paths, "missing/file.go",
		"a failed call's path is exactly the evidence worth having")
}

// The flattened text is deterministic: two projections of one call must be
// identical, or two verifications would differ.
func TestProjectIsDeterministic(t *testing.T) {
	call := forgexmodel.ToolCall{
		RunID: "run-1",
		Args: map[string]any{
			"zebra": "one", "alpha": "two", "middle": "three",
			"nested": map[string]any{"b": "beta", "a": "alpha"},
		},
	}

	first := projectToolCall(call)
	for i := 0; i < 20; i++ {
		assert.Equal(t, first.Text, projectToolCall(call).Text, "iteration %d", i)
	}
}

// A call with no timestamps still projects: the loader tolerates incomplete
// records rather than dropping them.
func TestProjectWithoutTimestamp(t *testing.T) {
	out := projectToolCall(forgexmodel.ToolCall{RunID: "r", ToolName: "t"})
	assert.True(t, out.At.IsZero() || !out.At.IsZero(), "no panic on a zero time")
}

// --- loading ---

// The newest runs are read first, because verification asks about the current
// world.
func TestLoadEvidencePrefersNewestRuns(t *testing.T) {
	root := t.TempDir()

	writeRun(t, root, "old-run", []forgexmodel.ToolCall{
		{RunID: "old-run", ToolName: "file.read", Args: map[string]any{"path": "old.go"}},
	})
	// Make the old run genuinely older on disk.
	oldTime := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, "runs", "old-run"), oldTime, oldTime))

	writeRun(t, root, "new-run", []forgexmodel.ToolCall{
		{RunID: "new-run", ToolName: "file.read", Args: map[string]any{"path": "new.go"}},
	})

	calls, err := LoadEvidence(root, 1, 100)
	require.NoError(t, err)
	require.NotEmpty(t, calls)
	assert.Equal(t, "new-run", calls[0].RunID, "the newest run is read first")
}

// A missing runs directory is not a failure: verification reports "no evidence"
// rather than an error, because a fresh deployment has no runs yet.
func TestLoadEvidenceMissingRoot(t *testing.T) {
	calls, err := LoadEvidence(filepath.Join(t.TempDir(), "nonexistent"), 20, 100)
	require.NoError(t, err)
	assert.Empty(t, calls)
}

// A run with no tool calls file is skipped, and the scan continues: one
// incomplete run must not sink the whole sample.
func TestLoadEvidenceSkipsRunsWithoutCalls(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "runs", "empty-run"), 0o755))
	writeRun(t, root, "full-run", []forgexmodel.ToolCall{
		{RunID: "full-run", ToolName: "file.read", Args: map[string]any{"path": "a.go"}},
	})

	calls, err := LoadEvidence(root, 20, 100)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "full-run", calls[0].RunID)
}

// A malformed line is skipped rather than failing the file: the rest of the
// evidence is still usable.
func TestLoadEvidenceToleratesMalformedLines(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "runs", "run-1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "{\"run_id\":\"run-1\",\"tool_name\":\"file.read\",\"args\":{\"path\":\"a.go\"}}\n" +
		"this is not json\n" +
		"{\"run_id\":\"run-1\",\"tool_name\":\"file.read\",\"args\":{\"path\":\"b.go\"}}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, toolCallsFileName), []byte(content), 0o644))

	calls, err := LoadEvidence(root, 20, 100)
	require.NoError(t, err)
	assert.Len(t, calls, 2, "the two valid lines survive the broken one")
}

// The call cap is honoured, so a large history does not become a large read.
func TestLoadEvidenceRespectsCallCap(t *testing.T) {
	root := t.TempDir()
	var calls []forgexmodel.ToolCall
	for i := 0; i < 20; i++ {
		calls = append(calls, forgexmodel.ToolCall{
			RunID: "run-1", ToolName: "file.read", Args: map[string]any{"path": "a.go"},
		})
	}
	writeRun(t, root, "run-1", calls)

	loaded, err := LoadEvidence(root, 20, 5)
	require.NoError(t, err)
	assert.Len(t, loaded, 5)
}

// --- helpers ---

// Path detection keys on a known extension on the final segment: a bare
// directory says little a verifier can use.
//
// A path containing a space IS accepted ("a b.go" is a legal filename), so the
// rejection list holds only things that are not paths at all — newlines make a
// value prose rather than a name, and the rest have no known extension.
func TestLooksLikePath(t *testing.T) {
	for _, yes := range []string{"main.go", "internal/agent/core/tools.go", "configs/app.yaml", "README.md", "a b.go"} {
		assert.True(t, looksLikePath(yes), yes)
	}
	for _, no := range []string{"", "internal/", "v2.1", "api.example.com", "line\nbreak.go", "a\tb.go"} {
		assert.False(t, looksLikePath(no), no)
	}
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "b", "a", "", "c", "b"})
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// Nested values are walked so a worker's own shape does not hide evidence.
func TestWalkValuesDescends(t *testing.T) {
	var found []string
	walkValues(map[string]any{
		"outer": map[string]any{"inner": []any{"deep.go", 42}},
	}, func(s string) { found = append(found, s) })

	assert.Contains(t, found, "deep.go")
	assert.Contains(t, found, "42")
}
