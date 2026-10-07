package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// staticLoop builds a loop whose context manager has the given budget, with
// the summariser scripted. Tests set keep/target directly on the manager: the
// wiring from LoopConfig is asserted separately below.
func staticLoop(t *testing.T, budget int, responses ...string) (*AgentLoop, *mockLLM) {
	t.Helper()
	if len(responses) == 0 {
		responses = []string{"summary of old turns"}
	}
	llm := &mockLLM{responses: responses}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), LoopConfig{MaxContextTokens: budget})
	return loop, llm
}

// bigToolObservation is a tool observation long enough to trip the S2 slim
// threshold (2000 chars).
func bigToolObservation(marker string) core.Message {
	return core.Message{
		Role:    "user",
		Content: fmt.Sprintf("[Tool \"file.read\" result]: %s %s", marker, strings.Repeat("payload ", 400)),
	}
}

// S2: an oversized tool observation is slimmed (head+tail+marker) and — when
// that alone gets under budget — NOTHING is summarised away.
func TestStaticCompactionSlimsToolOutputFirst(t *testing.T) {
	// Budget sits between "slimmed" and "raw": only slimming can fit it.
	raw := bigToolObservation("MARKER_HEAD")
	msgs := []core.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "start"}}
	for i := 0; i < 3; i++ {
		msgs = append(msgs, core.Message{Role: "assistant", Content: fmt.Sprintf("thinking %d", i)})
		msgs = append(msgs, raw)
	}
	rawTokens := EstimateTokens(msgs)

	loop, _ := staticLoop(t, 0)
	loop.ctxMgr = NewContextManager(rawTokens-20, loop.llm) // just under the raw size
	loop.ctxMgr.keep = 4

	out, err := loop.ctxMgr.CompactIfNeeded(context.Background(), msgs)
	require.NoError(t, err)
	require.Len(t, out, len(msgs), "no message was dropped — slimming sufficed")
	assert.Contains(t, joinedText(out), "slimmed for the context budget")
	assert.Contains(t, joinedText(out), "MARKER_HEAD", "the head survives")
	assert.Less(t, EstimateTokens(out), rawTokens, "the window actually shrank")
}

// S2 must never touch ordinary user text, however long: trimming a person's
// message is not the loop's call.
func TestStaticCompactionNeverSlimsUserText(t *testing.T) {
	huge := strings.Repeat("user prose ", 5000)
	msgs := []core.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: huge},
	}
	loop, _ := staticLoop(t, 200)
	out, err := loop.ctxMgr.CompactIfNeeded(context.Background(), msgs)
	require.NoError(t, err)
	assert.Contains(t, joinedText(out), huge,
		"a long user message is left alone (and no compaction can help with 2 messages)")
}

// S1 floor: the keep-tail widens so the LAST tool observation is never
// summarised away — the model must keep sight of the result it just acted on.
func TestStaticCompactionKeepsLastToolObservation(t *testing.T) {
	msgs := []core.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "old turn 1"},
		{Role: "assistant", Content: "old thinking"},
		{Role: "user", Content: "[Tool \"file.read\" result]: THE CRITICAL RESULT"},
		{Role: "assistant", Content: "newer thinking"},
		{Role: "user", Content: "newest turn"},
	}
	loop, _ := staticLoop(t, 30) // tiny budget: compaction is forced
	loop.ctxMgr.keep = 2         // a 2-message tail would exclude the observation

	out, err := loop.ctxMgr.CompactIfNeeded(context.Background(), msgs)
	require.NoError(t, err)

	assert.Contains(t, joinedText(out), "THE CRITICAL RESULT",
		"the last tool observation stayed in the window despite the small keep-tail")
	assert.Contains(t, joinedText(out), "[Conversation summary:")
}

// bulge returns content heavy enough that message size dominates the fixed
// summary allowance, which is what makes the S4 arithmetic in these tests
// predictable (a summary costs ~250 tokens; three-word turns would never
// outweigh it).
func bulge(tag string) string { return tag + " " + strings.Repeat("x", 200) }

// S4: the static path summarises only as far as needed — recent turns that
// already fit under the target are left alone rather than swept into the
// summary with the rest.
func TestStaticCompactionDropsOnlyWhatTheTargetRequires(t *testing.T) {
	msgs := []core.Message{{Role: "system", Content: "rules"}}
	for i := 0; i < 12; i++ {
		msgs = append(msgs, core.Message{Role: "user", Content: bulge(fmt.Sprintf("old turn %d", i))})
	}
	msgs = append(msgs, core.Message{Role: "user", Content: "RECENT_KEEP_ME"})

	loop, _ := staticLoop(t, 0)
	// Over budget (fits ~849 tokens of conversation), so compaction triggers;
	// the 60% target is reachable by summarising most — but not all — of it.
	loop.ctxMgr = NewContextManager(800, loop.llm)
	loop.ctxMgr.keep = 1
	loop.ctxMgr.compactTarget = 0.60

	out, err := loop.ctxMgr.CompactIfNeeded(context.Background(), msgs)
	require.NoError(t, err)
	require.Less(t, len(out), len(msgs), "something was summarised")
	assert.Contains(t, joinedText(out), "RECENT_KEEP_ME", "the newest turn survived")

	// Minimal-drop accounting: the planner stopped as soon as the projection
	// fit the target, so the newest OLD turns survive too. Sweeping the whole
	// history into the summary would have been the lazy implementation.
	assert.Contains(t, joinedText(out), "old turn 11", "the planner did not sweep the whole history")
}

// planCompaction is now genuinely shared by both paths: the same keep-tail and
// tool-observation floor apply whether the loop compacts statically or the
// model asks for it. The only difference is `explicit`, which drops the whole
// candidate range instead of stopping once the projection fits the target.
func TestPlanCompactionSharedByBothPaths(t *testing.T) {
	const historyLength = 20
	cm := NewContextManager(1400, &mockLLM{responses: []string{"s"}})
	cm.keep = 1
	cm.compactTarget = 0.60

	system := []core.Message{{Role: "system", Content: "rules"}}
	conv := make([]core.Message, 0, historyLength)
	for i := 0; i < historyLength-3; i++ {
		conv = append(conv, core.Message{Role: "user", Content: bulge(fmt.Sprintf("old %d", i))})
	}
	conv = append(conv,
		core.Message{Role: "user", Content: "[Tool \"x\" result]: " + bulge("latest observation")},
		core.Message{Role: "assistant", Content: bulge("tail 1")},
		core.Message{Role: "user", Content: bulge("tail 2")},
	)
	observationIndex := historyLength - 3

	// keep=1 would start the tail at the last message, but the floor pulls it
	// back to the observation — on BOTH paths.
	staticSummarize, staticKeep := cm.planCompaction(system, conv, false)
	require.NotEmpty(t, staticSummarize)
	require.Less(t, len(staticSummarize), observationIndex,
		"the static path stopped at the target instead of sweeping the history")
	assert.Contains(t, joinedText(staticKeep), "latest observation", "the floor held")

	explicitSummarize, explicitKeep := cm.planCompaction(system, conv, true)
	assert.Equal(t, observationIndex, len(explicitSummarize),
		"an explicit compaction drops the whole candidate range, up to the floor")
	assert.Greater(t, len(explicitSummarize), len(staticSummarize),
		"the model asking to compact clears more than the automatic trim")
	assert.Contains(t, joinedText(explicitKeep), "latest observation", "the floor holds on the explicit path too")
}

// Nothing may be summarised (tail already covers the history): the planner
// returns an empty range and the summariser is NOT called with nothing.
func TestPlanCompactionRefusesEmptyRange(t *testing.T) {
	cm := NewContextManager(10000, &mockLLM{responses: []string{"s"}})
	cm.keep = 10
	conv := []core.Message{{Role: "user", Content: "a"}, {Role: "user", Content: "b"}}
	summarize, keep := cm.planCompaction(nil, conv, false)
	assert.Empty(t, summarize)
	assert.Len(t, keep, 2)
}

// The model's explicit compact still refuses an empty history with an explicit
// error rather than calling the summariser for nothing.
func TestExplicitCompactRefusesNothingToDo(t *testing.T) {
	loop, llm := staticLoop(t, 100000, "unused")
	msgs := []core.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "only turn"}}

	result := loop.runContextTool(context.Background(), "context.compact", nil, &msgs, "s", 1)
	require.NotEmpty(t, result.Error)
	assert.Contains(t, result.Error, "nothing to compact")
	assert.Equal(t, 0, llm.callIdx, "the summariser was never called")
}

// The LoopConfig → ContextManager wiring is real (S1/S4's original defect was
// exactly a knob that existed but never reached the static path).
func TestLoopConfigWiresKeepAndTargetIntoContextManager(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{"s"}}, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxContextTokens: 1000, CompactKeepMessages: 9, ContextCompactTarget: 0.33})
	assert.Equal(t, 9, loop.ctxMgr.keep, "keep-count reached the context manager")
	assert.InDelta(t, 0.33, loop.ctxMgr.compactTarget, 1e-9, "target reached the context manager")

	// Defaults when unset.
	def := NewAgentLoop(&mockLLM{responses: []string{"s"}}, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxContextTokens: 1000})
	assert.Equal(t, defaultCompactKeepMessages, def.ctxMgr.keep)
	assert.InDelta(t, defaultCompactTarget, def.ctxMgr.compactTarget, 1e-9)
}

// A too-long conversation that the target can satisfy must NOT lose its recent
// turns: this is the user-visible point of S4.
func TestStaticCompactionRetainsRecentTurnsUnderTarget(t *testing.T) {
	msgs := []core.Message{{Role: "system", Content: "rules"}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, core.Message{Role: "user", Content: strings.Repeat(fmt.Sprintf("old%d ", i), 40)})
	}
	msgs = append(msgs, core.Message{Role: "user", Content: "RECENT_SURVIVOR"})

	total := EstimateTokens(msgs)
	loop, _ := staticLoop(t, 0)
	// Budget just under the current size, so we are over the line but the
	// target is reachable by dropping a few old turns only.
	loop.ctxMgr = NewContextManager(total-5, loop.llm)
	loop.ctxMgr.keep = 1
	loop.ctxMgr.compactTarget = 0.60

	out, err := loop.ctxMgr.CompactIfNeeded(context.Background(), msgs)
	require.NoError(t, err)
	assert.Less(t, len(out), len(msgs), "a compaction happened")
	assert.Contains(t, joinedText(out), "RECENT_SURVIVOR", "the newest turn survived")
	assert.LessOrEqual(t, EstimateTokens(out), int(float64(total-5)*0.60)+summaryEstimateTokens+20,
		"the result is near the target, not merely under the budget")
}
