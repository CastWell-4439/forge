package core

// Tool-output slimming profiles (the S2 layer of compaction).
//
// S2 evicts the cheapest thing first: an oversized TOOL observation is cut to
// head + tail, because tool output is the heaviest and most rebuildable content
// in a conversation. Ordinary user text is never touched — a person's long
// message is not ours to edit.
//
// The cut used to be three constants (threshold 2000, head 1500, tail 500). That
// is one answer to a question with several: WHERE the useful part of an output
// sits depends entirely on what produced it.
//
//	head-heavy   a file, a diff, a source listing — the top is the content
//	             and the bottom is usually a trailing newline
//	tail-heavy   a log, a test run, a stack trace — the END is the answer;
//	             the beginning is boilerplate that nobody reads
//	both         structured data whose shape is at both ends (JSON, a report)
//
// A single 1500/500 split serves the first case well and the other two badly.
// This is a real preference, not a knob for its own sake: the profile decides
// what the model can still see after eviction, and getting it wrong costs the
// model the exact information it needed.

// SlimProfile describes how to cut one oversized tool observation.
type SlimProfile struct {
	// Threshold is the content length above which slimming applies. Content at
	// or below it is passed through untouched.
	Threshold int
	// Head is how many bytes to keep from the start.
	Head int
	// Tail is how many bytes to keep from the end. Zero means "head only".
	Tail int
}

// Built-in profiles.
var (
	// SlimHeadHeavy suits sources, diffs and listings: keep the top.
	SlimHeadHeavy = SlimProfile{Threshold: 2000, Head: 1500, Tail: 500}
	// SlimTailHeavy suits logs, test output and stack traces: the answer is at
	// the end, so the tail gets the larger share.
	SlimTailHeavy = SlimProfile{Threshold: 2000, Head: 500, Tail: 1500}
	// SlimBalanced suits structured data whose shape appears at both ends.
	SlimBalanced = SlimProfile{Threshold: 2000, Head: 1000, Tail: 1000}
	// SlimHeadOnly suits content where a tail is actively misleading — a partial
	// last line reads like a complete one.
	SlimHeadOnly = SlimProfile{Threshold: 2000, Head: 2000, Tail: 0}
)

// DefaultSlimProfile is the historical behaviour, kept as the default so an
// unconfigured deployment is byte-for-byte what it was.
var DefaultSlimProfile = SlimHeadHeavy

// SlimmingMode selects how profiles are chosen.
type SlimmingMode string

const (
	// SlimmingUniform applies one profile to every tool. It is the default and
	// the pre-existing behaviour.
	SlimmingUniform SlimmingMode = "uniform"
	// SlimmingByTool chooses a profile per tool name, falling back to the
	// uniform profile for tools with no entry. This is the useful mode: the
	// tools in one deployment are known, and so is where their output matters.
	SlimmingByTool SlimmingMode = "by_tool"
)

// ToolSlimConfig is the resolved slimming configuration.
type ToolSlimConfig struct {
	Mode    SlimmingMode
	Default SlimProfile
	// ByTool overrides the default for named tools. Only read in by_tool mode.
	ByTool map[string]SlimProfile
}

// DefaultToolSlimConfig returns the unconfigured behaviour: one head-heavy
// profile for everything, which is exactly what the constants used to be.
func DefaultToolSlimConfig() ToolSlimConfig {
	return ToolSlimConfig{
		Mode:    SlimmingUniform,
		Default: DefaultSlimProfile,
	}
}

// Normalize fills in anything unset, so a partially configured deployment still
// has a complete policy.
func (c ToolSlimConfig) Normalize() ToolSlimConfig {
	switch c.Mode {
	case SlimmingByTool, SlimmingUniform:
	default:
		c.Mode = SlimmingUniform
	}
	c.Default = c.Default.withDefaults()
	for name, p := range c.ByTool {
		c.ByTool[name] = p.withDefaults()
	}
	return c
}

// ProfileFor returns the profile to use for a tool.
func (c ToolSlimConfig) ProfileFor(tool string) SlimProfile {
	if c.Mode == SlimmingByTool && c.ByTool != nil {
		if p, ok := c.ByTool[tool]; ok {
			return p.withDefaults()
		}
	}
	return c.Default.withDefaults()
}

// withDefaults repairs a profile whose fields are unset or nonsensical.
//
// A zero threshold would slim EVERY tool observation, including tiny ones —
// turning a config typo into "the model loses most tool output". Falling back
// to the default profile is the safe reading of an incomplete value.
func (p SlimProfile) withDefaults() SlimProfile {
	if p.Threshold <= 0 {
		p.Threshold = DefaultSlimProfile.Threshold
	}
	if p.Head < 0 {
		p.Head = 0
	}
	if p.Tail < 0 {
		p.Tail = 0
	}
	// Both ends zero would delete the content and leave only the marker, which
	// is worse than not slimming at all. Treat it as "unset" and keep the head.
	if p.Head == 0 && p.Tail == 0 {
		p.Head = DefaultSlimProfile.Head
		p.Tail = DefaultSlimProfile.Tail
	}
	return p
}

// NamedProfile resolves a profile name, for configuration that names one
// instead of spelling out three numbers.
func NamedProfile(name string) (SlimProfile, bool) {
	switch name {
	case "head_heavy", "head":
		return SlimHeadHeavy, true
	case "tail_heavy", "tail":
		return SlimTailHeavy, true
	case "balanced", "both":
		return SlimBalanced, true
	case "head_only":
		return SlimHeadOnly, true
	default:
		return SlimProfile{}, false
	}
}
