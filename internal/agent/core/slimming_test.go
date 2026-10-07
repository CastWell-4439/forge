package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The unconfigured policy must be exactly the historical constants: a
// deployment that sets nothing must see byte-for-byte what it saw before.
func TestDefaultSlimConfigMatchesHistoricalConstants(t *testing.T) {
	cfg := DefaultToolSlimConfig()
	assert.Equal(t, SlimmingUniform, cfg.Mode)
	assert.Equal(t, 2000, cfg.Default.Threshold)
	assert.Equal(t, 1500, cfg.Default.Head)
	assert.Equal(t, 500, cfg.Default.Tail)
	assert.Nil(t, cfg.ByTool)
}

// --- named profiles ---

// Each profile answers a different question about where the useful part sits.
func TestNamedProfiles(t *testing.T) {
	head, ok := NamedProfile("head_heavy")
	assert.True(t, ok)
	assert.Greater(t, head.Head, head.Tail, "a source listing's content is at the top")

	tail, ok := NamedProfile("tail_heavy")
	assert.True(t, ok)
	assert.Greater(t, tail.Tail, tail.Head, "a log's answer is at the bottom")

	both, ok := NamedProfile("balanced")
	assert.True(t, ok)
	assert.Equal(t, both.Head, both.Tail)

	only, ok := NamedProfile("head_only")
	assert.True(t, ok)
	assert.Zero(t, only.Tail, "a partial last line can read as a complete one")

	// Aliases exist so the common short forms work.
	for _, alias := range []string{"head", "tail", "both"} {
		_, ok := NamedProfile(alias)
		assert.True(t, ok, alias)
	}

	_, ok = NamedProfile("sideways")
	assert.False(t, ok)
	_, ok = NamedProfile("")
	assert.False(t, ok)
}

// --- profile selection ---

// Uniform mode ignores tool names entirely: one policy for everything, which is
// the pre-existing behaviour.
func TestUniformModeIgnoresToolName(t *testing.T) {
	cfg := ToolSlimConfig{
		Mode:    SlimmingUniform,
		Default: SlimTailHeavy,
		ByTool:  map[string]SlimProfile{"file.read": SlimHeadOnly},
	}.Normalize()

	// Even a tool with an entry gets the default: the map is only consulted in
	// by_tool mode, so a leftover map cannot change behaviour.
	assert.Equal(t, SlimTailHeavy.withDefaults(), cfg.ProfileFor("file.read"))
	assert.Equal(t, SlimTailHeavy.withDefaults(), cfg.ProfileFor("anything"))
}

// by_tool mode picks per tool and falls back to the default for the rest. This
// is the mode worth having: one deployment knows its tools, and a log tool and
// a file tool want opposite cuts.
func TestByToolModeSelectsPerTool(t *testing.T) {
	cfg := ToolSlimConfig{
		Mode:    SlimmingByTool,
		Default: SlimHeadHeavy,
		ByTool: map[string]SlimProfile{
			"shell.run":    SlimTailHeavy, // test output: the answer is last
			"file.read":    SlimHeadHeavy, // source: the answer is first
			"data.query":   SlimBalanced,  // structured: both ends matter
			"web.fetch":    SlimHeadOnly,  // a partial HTML tail is misleading
			"code.execute": SlimTailHeavy, // stack traces
		},
	}.Normalize()

	assert.Equal(t, SlimTailHeavy.withDefaults(), cfg.ProfileFor("shell.run"))
	assert.Equal(t, SlimHeadHeavy.withDefaults(), cfg.ProfileFor("file.read"))
	assert.Equal(t, SlimBalanced.withDefaults(), cfg.ProfileFor("data.query"))
	assert.Equal(t, SlimHeadOnly.withDefaults(), cfg.ProfileFor("web.fetch"))
	assert.Equal(t, SlimTailHeavy.withDefaults(), cfg.ProfileFor("code.execute"))

	// An unlisted tool gets the default rather than nothing.
	assert.Equal(t, SlimHeadHeavy.withDefaults(), cfg.ProfileFor("git.diff"))
}

// A by_tool config with no map behaves as uniform: the mode says "look up per
// tool" but there is nothing to look up, so the default must apply.
func TestByToolModeWithoutMapFallsBack(t *testing.T) {
	cfg := ToolSlimConfig{Mode: SlimmingByTool, Default: SlimBalanced}.Normalize()
	assert.Equal(t, SlimBalanced.withDefaults(), cfg.ProfileFor("file.read"))
}

// --- repair of incomplete values ---

// A zero threshold would slim EVERY tool observation, including tiny ones —
// turning a config typo into "the model loses most of its tool output". The
// safe reading of an incomplete value is the default.
func TestZeroThresholdFallsBackToDefault(t *testing.T) {
	p := SlimProfile{}.withDefaults()
	assert.Equal(t, DefaultSlimProfile.Threshold, p.Threshold)
	assert.Equal(t, DefaultSlimProfile.Head, p.Head)
	assert.Equal(t, DefaultSlimProfile.Tail, p.Tail)

	// The same applies to a profile reached through a config.
	cfg := ToolSlimConfig{
		Mode:    SlimmingByTool,
		Default: SlimHeadHeavy,
		ByTool:  map[string]SlimProfile{"file.read": {}},
	}.Normalize()
	got := cfg.ProfileFor("file.read")
	assert.Equal(t, DefaultSlimProfile.Threshold, got.Threshold)
	assert.NotZero(t, got.Head+got.Tail, "a profile that keeps nothing is not a profile")
}

// Head and tail both zero would delete the content and leave only the marker,
// which is worse than not slimming. It is treated as unset.
func TestBothEndsZeroFallsBack(t *testing.T) {
	p := SlimProfile{Threshold: 100, Head: 0, Tail: 0}.withDefaults()
	assert.Equal(t, DefaultSlimProfile.Head, p.Head)
	assert.Equal(t, DefaultSlimProfile.Tail, p.Tail)
	assert.Equal(t, 100, p.Threshold, "an explicitly configured threshold is kept")
}

// Head-only is expressible: Tail 0 with a positive Head is deliberate, not an
// error, so it must NOT be "repaired" into head+tail.
func TestHeadOnlyIsRespected(t *testing.T) {
	p := SlimProfile{Threshold: 100, Head: 200, Tail: 0}.withDefaults()
	assert.Equal(t, 200, p.Head)
	assert.Zero(t, p.Tail, "head-only survives normalization")
}

// Negative values are nonsense and clamped to zero rather than kept.
func TestNegativeValuesClamped(t *testing.T) {
	p := SlimProfile{Threshold: 100, Head: -50, Tail: -50}.withDefaults()
	assert.GreaterOrEqual(t, p.Head, 0)
	assert.GreaterOrEqual(t, p.Tail, 0)
}

// An unknown mode falls back to uniform, which is the historical behaviour.
func TestUnknownModeFallsBackToUniform(t *testing.T) {
	cfg := ToolSlimConfig{Mode: "per_phase", Default: SlimBalanced}.Normalize()
	assert.Equal(t, SlimmingUniform, cfg.Mode)
	assert.Equal(t, SlimBalanced.withDefaults(), cfg.ProfileFor("file.read"))
}

// Normalize is idempotent: normalizing twice changes nothing, so a config that
// passes through two layers cannot drift.
func TestNormalizeIsIdempotent(t *testing.T) {
	once := ToolSlimConfig{
		Mode:    SlimmingByTool,
		Default: SlimTailHeavy,
		ByTool:  map[string]SlimProfile{"shell.run": SlimBalanced},
	}.Normalize()
	twice := once.Normalize()
	assert.Equal(t, once.Mode, twice.Mode)
	assert.Equal(t, once.Default, twice.Default)
	assert.Equal(t, once.ByTool, twice.ByTool)
}
