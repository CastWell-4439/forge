package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- modes ---

// Off is the default for everything unset or unrecognised. A typo in a
// deployment variable must not enable a capability nobody asked for.
func TestSubagentModeDefaultsToOff(t *testing.T) {
	assert.Equal(t, SubagentOff, NormalizeSubagentMode(""))
	assert.Equal(t, SubagentOff, NormalizeSubagentMode("on"))
	assert.Equal(t, SubagentOff, NormalizeSubagentMode("true"))
	assert.Equal(t, SubagentOff, NormalizeSubagentMode("ISOLATED")) // case matters
	assert.Equal(t, SubagentOff, NormalizeSubagentMode("continuable "))

	assert.False(t, SubagentOff.Enabled())
	assert.True(t, SubagentIsolated.Enabled())
	assert.True(t, SubagentContinuable.Enabled())
}

func TestSubagentModeRecognisedValues(t *testing.T) {
	assert.Equal(t, SubagentIsolated, NormalizeSubagentMode("isolated"))
	assert.Equal(t, SubagentContinuable, NormalizeSubagentMode("continuable"))
	assert.False(t, SubagentIsolated.Continuable())
	assert.True(t, SubagentContinuable.Continuable())
}

// --- report shape (the second orthogonal axis) ---

// Summary is the default: "what did it do" is the question a parent asks, and
// full steps would spend the parent's context on the child's.
func TestSubagentReportDefaultsToSummary(t *testing.T) {
	assert.Equal(t, SubagentReportSummary, NormalizeSubagentReport(""))
	assert.Equal(t, SubagentReportSummary, NormalizeSubagentReport("everything"))
	assert.Equal(t, SubagentReportAnswer, NormalizeSubagentReport("answer"))
	assert.Equal(t, SubagentReportSteps, NormalizeSubagentReport("steps"))
}

// --- limits ---

// The default allows exactly one level: the top-level agent may delegate, and a
// child may not delegate again. Recursion is the failure this prevents.
func TestDefaultDepthAllowsOneLevel(t *testing.T) {
	assert.Equal(t, 1, DefaultSubagentMaxDepth)
	assert.Equal(t, 1, DefaultSubagentMaxConcurrent, "fan-out is a choice, not a default")
}

// A child gets half its parent's budget, so one subtask cannot drain the
// parent. The floor keeps a small parent's child useful rather than useless.
func TestChildStepsDerivedFromParent(t *testing.T) {
	assert.Equal(t, 10, DefaultChildSteps(20))
	assert.Equal(t, 0, DefaultChildSteps(0), "no parent limit means no child limit invented")
	assert.Equal(t, 0, DefaultChildSteps(-5))

	// A parent with a tiny budget still yields a usable child.
	assert.Equal(t, DefaultChildStepsFloor, DefaultChildSteps(4))
	assert.Equal(t, DefaultChildStepsFloor, DefaultChildSteps(2))
}

// --- config normalization ---

func TestNormalizeSubagentConfig(t *testing.T) {
	cfg := NormalizeSubagentConfig(SubagentConfig{})
	assert.Equal(t, SubagentOff, cfg.Mode)
	assert.Equal(t, SubagentReportSummary, cfg.Report)
	assert.Equal(t, DefaultSubagentMaxConcurrent, cfg.Limits.MaxConcurrent)
	assert.Zero(t, cfg.Limits.MaxDepth, "an unset depth stays 0: normalization invents no permission")

	cfg = NormalizeSubagentConfig(SubagentConfig{
		Mode:   "isolated",
		Report: "steps",
		Limits: SubagentLimits{MaxDepth: 3, MaxConcurrent: 0},
	})
	assert.Equal(t, SubagentIsolated, cfg.Mode)
	assert.Equal(t, SubagentReportSteps, cfg.Report)
	assert.Equal(t, 3, cfg.Limits.MaxDepth)
	assert.Equal(t, DefaultSubagentMaxConcurrent, cfg.Limits.MaxConcurrent)

	// A negative depth is nonsense and clamped to the forbidding value.
	cfg = NormalizeSubagentConfig(SubagentConfig{Mode: SubagentIsolated, Limits: SubagentLimits{MaxDepth: -1}})
	assert.Zero(t, cfg.Limits.MaxDepth)
}

// The three axes are independent: the combinations that a single "mode" enum
// could not express must all survive normalization.
func TestAxesAreIndependent(t *testing.T) {
	combos := []struct {
		mode   SubagentMode
		report SubagentReport
	}{
		{SubagentIsolated, SubagentReportSteps},     // one-shot, fully transparent
		{SubagentContinuable, SubagentReportAnswer}, // continuable, answer only
		{SubagentContinuable, SubagentReportSteps},
		{SubagentIsolated, SubagentReportAnswer},
	}
	for _, c := range combos {
		cfg := NormalizeSubagentConfig(SubagentConfig{Mode: c.mode, Report: c.report})
		assert.Equal(t, c.mode, cfg.Mode)
		assert.Equal(t, c.report, cfg.Report)
	}
}
