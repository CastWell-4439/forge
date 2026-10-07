package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// longObservation builds a prompt-path tool observation of the given length.
// The prompt path echoes observations as user text with a "[tool: name]" prefix,
// which is also how the per-tool profile finds the tool name.
func longObservation(tool string, n int) core.Message {
	body := strings.Repeat("x", n)
	return core.Message{Role: "user", Content: "[Tool \"" + tool + "\" result]: " + body}
}

// The default policy must reproduce the old fixed behaviour exactly: a
// deployment that configures nothing sees what it saw before.
func TestDefaultSlimmingIsUnchanged(t *testing.T) {
	cm := NewContextManager(100000, nil)

	long := longObservation("file.read", 10000)
	out := cm.slimToolObservations([]core.Message{long})
	require.Len(t, out, 1)
	assert.Less(t, len(out[0].Content), 10000, "it was slimmed")
	assert.Contains(t, out[0].Content, "slimmed for the context budget")

	// Content at or below the threshold is untouched.
	short := longObservation("file.read", 1500)
	out = cm.slimToolObservations([]core.Message{short})
	assert.Equal(t, short.Content, out[0].Content, "short output is not edited")
}

// Ordinary user text is never slimmed, however long: a person's message is not
// ours to edit. This is the boundary the S2 layer must not cross.
func TestUserTextIsNeverSlimmed(t *testing.T) {
	cm := NewContextManager(100000, nil)
	user := core.Message{Role: "user", Content: strings.Repeat("important ", 2000)}
	out := cm.slimToolObservations([]core.Message{user})
	assert.Equal(t, user.Content, out[0].Content)
}

// A tail-heavy profile keeps the END of the output, which is where a log's
// answer is. The head/tail asymmetry is the whole point of the setting.
func TestTailHeavyProfileKeepsTheEnd(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.SetToolSlim(core.ToolSlimConfig{
		Mode:    core.SlimmingByTool,
		Default: core.SlimHeadHeavy,
		ByTool:  map[string]core.SlimProfile{"shell.run": core.SlimTailHeavy},
	})

	// Distinct head and tail so which one survived is unambiguous.
	body := "HEADMARK" + strings.Repeat("m", 6000) + "TAILMARK"
	msg := core.Message{Role: "user", Content: "[Tool \"shell.run\" result]: " + body}

	out := cm.slimToolObservations([]core.Message{msg})
	require.Len(t, out, 1)
	assert.Contains(t, out[0].Content, "TAILMARK", "the end survived (it is the answer)")
	assert.Contains(t, out[0].Content, "HEADMARK", "the start survived too, just less of it")
	assert.Contains(t, out[0].Content, "slimmed for the context budget")
}

// The same output through a head-heavy profile keeps a different proportion.
// Same input, different policy, different result — that is what "configurable"
// has to mean to be worth anything.
func TestProfileChangesWhatSurvives(t *testing.T) {
	body := "HEADMARK" + strings.Repeat("m", 6000) + "TAILMARK"
	msg := core.Message{Role: "user", Content: "[Tool \"shell.run\" result]: " + body}

	head := NewContextManager(100000, nil)
	head.SetToolSlim(core.ToolSlimConfig{Default: core.SlimHeadHeavy})
	headOut := head.slimToolObservations([]core.Message{msg})

	tail := NewContextManager(100000, nil)
	tail.SetToolSlim(core.ToolSlimConfig{Default: core.SlimTailHeavy})
	tailOut := tail.slimToolObservations([]core.Message{msg})

	require.Len(t, headOut, 1)
	require.Len(t, tailOut, 1)
	assert.NotEqual(t, headOut[0].Content, tailOut[0].Content,
		"the profile decides what the model can still see")
}

// Head-only keeps no tail, and the marker still explains what happened so the
// model does not read the cut as the content ending.
func TestHeadOnlyProfile(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.SetToolSlim(core.ToolSlimConfig{Default: core.SlimHeadOnly})

	body := "HEADMARK" + strings.Repeat("m", 6000) + "TAILMARK"
	msg := core.Message{Role: "user", Content: "[Tool \"file.read\" result]: " + body}
	out := cm.slimToolObservations([]core.Message{msg})
	require.Len(t, out, 1)
	assert.Contains(t, out[0].Content, "HEADMARK")
	assert.NotContains(t, out[0].Content, "TAILMARK", "head-only keeps no tail")
	assert.Contains(t, out[0].Content, "slimmed for the context budget")
}

// A threshold is per profile, so a tool whose output is usually large can be
// slimmed earlier or later than the default.
func TestPerProfileThreshold(t *testing.T) {
	cm := NewContextManager(100000, nil)
	cm.SetToolSlim(core.ToolSlimConfig{
		Mode:    core.SlimmingByTool,
		Default: core.SlimHeadHeavy, // threshold 2000
		ByTool: map[string]core.SlimProfile{
			"web.fetch": {Threshold: 500, Head: 400, Tail: 100},
		},
	})

	// 1000 chars: above the web.fetch threshold, below the default one.
	body := strings.Repeat("w", 1000)
	fetched := cm.slimToolObservations([]core.Message{{Role: "user", Content: "[Tool \"web.fetch\" result]: " + body}})
	assert.Contains(t, fetched[0].Content, "slimmed", "web.fetch slims at 500")

	read := cm.slimToolObservations([]core.Message{{Role: "user", Content: "[Tool \"file.read\" result]: " + body}})
	assert.NotContains(t, read[0].Content, "slimmed", "file.read still slims at 2000")
}

// The tool name is recovered from the observation prefix. An observation whose
// name cannot be read falls back to the default profile rather than guessing —
// applying the wrong profile is worse than applying the general one.
func TestToolNameRecovery(t *testing.T) {
	assert.Equal(t, "file.read", toolNameOf(core.Message{Role: "user", Content: "[Tool \"file.read\" result]: body"}))
	assert.Equal(t, "shell.run", toolNameOf(core.Message{Role: "user", Content: "[Tool \"shell.run\" result]: body"}))
	assert.Equal(t, "git.diff", toolNameOf(core.Message{Role: "user", Content: "[Tool \"git.diff\" result]: body"}))

	// Native path: role "tool" does not repeat the name in the content.
	assert.Empty(t, toolNameOf(core.Message{Role: "tool", Content: "just output"}))

	// Not an observation at all.
	assert.Empty(t, toolNameOf(core.Message{Role: "user", Content: "plain question"}))
	assert.Empty(t, toolNameOf(core.Message{Role: "user", Content: ""}))
	assert.Empty(t, toolNameOf(core.Message{Role: "user", Content: "[Tool unterminated"}))
	assert.Empty(t, toolNameOf(core.Message{Role: "user", Content: "[Tool \"unclosed result]: x"}))
}

// The slimming policy reaches the manager through LoopConfig: a field nobody
// reads would be the "has a skeleton, no power" pattern.
func TestLoopConfigSlimReachesTheManager(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.ToolSlim = core.ToolSlimConfig{Default: core.SlimTailHeavy}
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), cfg)

	got := loop.ctxMgr.slim.ProfileFor("anything")
	assert.Equal(t, core.SlimTailHeavy.Threshold, got.Threshold)
	assert.Equal(t, core.SlimTailHeavy.Head, got.Head)
	assert.Equal(t, core.SlimTailHeavy.Tail, got.Tail)
}

// The no-progress threshold is configurable, and the zero value keeps the
// historical default.
func TestNoProgressThresholdIsConfigurable(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), DefaultLoopConfig())
	assert.Equal(t, DefaultNoProgressThreshold, loop.noProgressThreshold(), "0 means the default")

	cfg := DefaultLoopConfig()
	cfg.NoProgressThreshold = 7
	loop = NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), cfg)
	assert.Equal(t, 7, loop.noProgressThreshold(), "an explicit value is honoured")

	cfg.NoProgressThreshold = -1
	loop = NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), cfg)
	// A negative value resolves to 0, which is the "disabled" signal callers
	// test for. The config's 0 means "default", so the two meanings of zero are
	// kept apart here rather than at every call site.
	assert.Zero(t, loop.noProgressThreshold(), "negative disables the streak stop")
}
