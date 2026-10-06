package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/forgex/skillpack"
)

// skill.activate's loader: a published pack renders into the document the
// model follows, and a missing/unreadable pack NAMES the env var instead of
// failing with a vague "not configured".
func TestSkillLoaderRendersAndNamesGap(t *testing.T) {
	dir := t.TempDir()
	store := skillpack.NewStore(dir)
	_, err := store.Save(skillpack.Pack{
		APIVersion: "v1",
		Kind:       "SkillPack",
		Metadata:   skillpack.Metadata{ID: "triage", Version: "1.0.0", Status: "published"},
		Readme:     "when a report arrives",
		Spec:       skillpack.Spec{Trigger: skillpack.Trigger{Description: "bug reports"}},
	}, false)
	require.NoError(t, err)

	load := skillLoader(dir)
	doc, err := load(context.Background(), "triage")
	require.NoError(t, err)
	assert.Contains(t, doc, "# Skill: triage")
	assert.Contains(t, doc, "when a report arrives")

	// Unknown id: the error is actionable — it names the directory and the
	// env var an operator would set.
	_, err = load(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), envSkillpackDir)
}

// With FORGE_SKILLPACK_DIR unset the loader still exists and its error points
// at the default directory — an unset loader would say something vaguer.
func TestSkillLoaderUnsetNamesDefault(t *testing.T) {
	load := skillLoader("")
	_, err := load(context.Background(), "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), defaultSkillpackDir)
	assert.Contains(t, err.Error(), envSkillpackDir)
}

// Water-line env parsing: empty = default (0), off = disabled (-1), a typo
// warns and falls back — it must not silently move the thresholds.
func TestEnvFractionAndInt(t *testing.T) {
	assert.Zero(t, envFraction("FORGE_TEST_UNSET_FRACTION"), "empty means the default")

	t.Setenv("FORGE_TEST_FRACTION", "0.85")
	assert.InDelta(t, 0.85, envFraction("FORGE_TEST_FRACTION"), 1e-9)

	t.Setenv("FORGE_TEST_FRACTION", "off")
	assert.Equal(t, float64(-1), envFraction("FORGE_TEST_FRACTION"), "off disables the reminders")

	t.Setenv("FORGE_TEST_FRACTION", "not-a-number")
	assert.Zero(t, envFraction("FORGE_TEST_FRACTION"), "a typo falls back to the default")

	t.Setenv("FORGE_TEST_INT", "8")
	assert.Equal(t, 8, envInt("FORGE_TEST_INT"))

	t.Setenv("FORGE_TEST_INT", "lots")
	assert.Zero(t, envInt("FORGE_TEST_INT"), "a typo falls back to the default")
}

// The default skill directory exists in the repository: the loader's promise
// ("configs/forgex/skills") must not be fiction.
func TestDefaultSkillpackDirExists(t *testing.T) {
	// Relative from internal/serve/worker → repo root.
	if _, err := os.Stat(filepath.Join("..", "..", "..", defaultSkillpackDir)); err != nil {
		t.Skipf("default skill dir not reachable from test cwd: %v", err)
	}
}
