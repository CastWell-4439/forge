package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// --- C3: relevant tool injection ---

// registryWithTools builds a registry of n tools, all read-class so the
// permission gate stays out of the way.
func registryWithTools(t *testing.T, names ...string) *workers.ToolRegistry {
	t.Helper()
	reg := workers.NewToolRegistry()
	for _, name := range names {
		require.NoError(t, reg.Register(&workers.ToolDef{
			Name: name, Description: "does " + name, Effect: workers.EffectRead, Idempotent: true,
		}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		}))
	}
	return reg
}

// The whole point of C3: the prompt carries a relevant subset, and it says how
// many tools are hidden and how to find them. A silent cutoff would be an
// invisible capability loss.
func TestPromptCarriesRelevantSubsetAndSaysWhatIsHidden(t *testing.T) {
	reg := registryWithTools(t,
		"file.read", "file.write", "git.status", "git.log", "web.fetch", "web.search",
		"shell.run", "data.query", "code.execute", "ask.user", "skill.activate", "knowledge.search",
		"context.remaining", "context.compact", "context.recall", "agent.pause", "agent.query", "tool.search",
	)
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 6
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	visible, hidden := loop.selectVisibleTools("read the file please", map[string]bool{})
	require.LessOrEqual(t, len(visible), 6, "the visible set is capped")
	assert.Greater(t, hidden, 0, "some tools were hidden")

	rendered := renderToolList(visible, hidden)
	assert.Contains(t, rendered, "more tool(s) are not listed here", "the cutoff is stated")
	assert.Contains(t, rendered, "tool.search", "and the way to find them is given")
}

// Relevance decides: a task about reading files must surface the file tools
// ahead of unrelated ones.
func TestRelevantToolsRankFirst(t *testing.T) {
	reg := registryWithTools(t,
		"file.read", "git.status", "web.search", "data.query",
		"context.remaining", "context.compact", "context.recall", "tool.search",
	)
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 5
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	visible, _ := loop.selectVisibleTools("please read the file and report", map[string]bool{})
	names := make([]string, 0, len(visible))
	for _, def := range visible {
		names = append(names, def.Name)
	}
	assert.Contains(t, names, "file.read", "a tool matching the task text is visible")
}

// The escape hatches are NEVER filtered out, whatever the relevance score: a
// run that cannot see agent.pause cannot ask for help, and one that cannot see
// tool.search cannot discover what it is missing.
func TestMetaToolsAlwaysVisible(t *testing.T) {
	reg := registryWithTools(t,
		"file.read", "git.status", "web.search", "data.query", "shell.run",
		"context.remaining", "context.compact", "context.recall",
		"agent.pause", "agent.query", "tool.search",
	)
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 5 // smaller than the meta set itself
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	visible, _ := loop.selectVisibleTools("something entirely unrelated", map[string]bool{})
	names := map[string]bool{}
	for _, def := range visible {
		names[def.Name] = true
	}
	for _, meta := range alwaysVisibleTools {
		assert.True(t, names[meta], "%s must always be visible", meta)
	}
}

// A negative limit disables filtering: the pre-C3 behaviour stays reachable.
func TestNegativeLimitListsEverything(t *testing.T) {
	reg := registryWithTools(t, "a.read", "b.read", "c.read")
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = -1
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	visible, hidden := loop.selectVisibleTools("anything", map[string]bool{})
	assert.Len(t, visible, 3, "no filtering when disabled")
	assert.Zero(t, hidden)
}

// A registry that already fits is left alone (no pointless truncation).
func TestSmallRegistryIsNotFiltered(t *testing.T) {
	reg := registryWithTools(t, "file.read", "git.status")
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), DefaultLoopConfig())

	visible, hidden := loop.selectVisibleTools("anything at all", map[string]bool{})
	assert.Len(t, visible, 2)
	assert.Zero(t, hidden)
}

// Already-used tools rank highest: the run is doing that kind of work.
func TestUsedToolsRankHighest(t *testing.T) {
	reg := registryWithTools(t,
		"file.read", "git.status", "web.search", "data.query",
		"context.remaining", "context.compact", "context.recall", "tool.search",
	)
	ranked := rankTools(reg.ListTools(), "unrelated text", map[string]bool{"git.status": true})
	require.NotEmpty(t, ranked)
	assert.Equal(t, "git.status", ranked[0].Name, "a tool the run already used comes first")
}

// Ordering is deterministic, which keeps the prompt prefix cacheable: the same
// set must always render the same way.
func TestVisibleToolOrderIsStable(t *testing.T) {
	reg := registryWithTools(t, "file.read", "git.status", "web.search", "data.query",
		"context.remaining", "context.compact", "context.recall", "tool.search")
	cfg := DefaultLoopConfig()
	cfg.VisibleTools = 5
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}}, NewToolRouter(reg), cfg)

	first, _ := loop.selectVisibleTools("read a file", map[string]bool{})
	second, _ := loop.selectVisibleTools("read a file", map[string]bool{})
	require.Len(t, first, len(second))
	for i := range first {
		assert.Equal(t, first[i].Name, second[i].Name, "position %d is stable", i)
	}
	for i := 1; i < len(first); i++ {
		assert.Less(t, first[i-1].Name, first[i].Name, "and sorted by name")
	}
}

// --- tool.search ---

// The discovery tool answers from the registry, so what the prompt hid is
// still reachable.
func TestToolSearchFindsHiddenTools(t *testing.T) {
	reg := registryWithTools(t, "file.read", "git.status", "deploy.production")
	llm := &capturingLLM{responses: []string{
		`{"thought":"look for it","action":{"name":"tool.search","params":{"query":"deploy"}}}`,
		finalAnswer,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(reg), DefaultLoopConfig())

	result, err := loop.Run(context.Background(), "search-1", "deploy something")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	observed := joinedText(llm.calls[1])
	assert.Contains(t, observed, "deploy.production", "the hidden tool was found")
	assert.Contains(t, observed, `"status":"ok"`)
}

// Name matches rank above description matches: asking for "git" means the git
// tools, not every tool whose prose mentions git.
func TestToolSearchRanksNameMatchesFirst(t *testing.T) {
	reg := workers.NewToolRegistry()
	require.NoError(t, reg.Register(&workers.ToolDef{
		Name: "unrelated.tool", Description: "this description mentions git prominently",
		Effect: workers.EffectRead,
	}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil }))
	require.NoError(t, reg.Register(&workers.ToolDef{
		Name: "git.log", Description: "reads history", Effect: workers.EffectRead,
	}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil }))

	matches := workers.SearchTools(reg, "git", 10)
	require.Len(t, matches, 2)
	assert.Equal(t, "git.log", matches[0].Name, "the name match wins")
}

// No match is reported as such, not as an empty success — the model must be
// able to tell "no such tool" from "the search failed".
func TestToolSearchNoMatchStatus(t *testing.T) {
	reg := registryWithTools(t, "file.read")
	llm := &capturingLLM{responses: []string{
		`{"thought":"look","action":{"name":"tool.search","params":{"query":"nonexistent"}}}`,
		finalAnswer,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(reg), DefaultLoopConfig())

	_, err := loop.Run(context.Background(), "search-2", "task")
	require.NoError(t, err)
	assert.Contains(t, joinedText(llm.calls[1]), `"status":"no_match"`)
}

// A missing query is refused with a message, not an empty result.
func TestToolSearchRequiresQuery(t *testing.T) {
	reg := registryWithTools(t, "file.read")
	llm := &capturingLLM{responses: []string{
		`{"thought":"look","action":{"name":"tool.search","params":{}}}`,
		finalAnswer,
	}}
	loop := NewAgentLoop(llm, NewToolRouter(reg), DefaultLoopConfig())

	_, err := loop.Run(context.Background(), "search-3", "task")
	require.NoError(t, err)
	assert.Contains(t, joinedText(llm.calls[1]), "missing required param 'query'")
}

// tool.search is a read-class meta tool: it must never be blocked by the
// permission gate, or a low-authority run could not discover its own tools.
func TestToolSearchIsNeverGated(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.Authority = core.AuthorityL0 // nothing may execute
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), cfg)

	_, ok := loop.checkToolAuthority(ToolSearchToolName)
	assert.True(t, ok, "discovery is never gated")
}

// The search result tells the model what a tool DOES, so it can reason about
// asking for it before calling it.
func TestToolSearchReportsEffect(t *testing.T) {
	reg := workers.NewToolRegistry()
	require.NoError(t, reg.Register(&workers.ToolDef{
		Name: "danger.purge", Description: "removes everything", Effect: workers.EffectDelete,
	}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) { return nil, nil }))

	matches := workers.SearchTools(reg, "purge", 5)
	require.Len(t, matches, 1)
	assert.Equal(t, "delete", matches[0].Effect, "the effect travels with the match")
}

// --- ranking helpers ---

func TestTaskWordsDropsShortNoise(t *testing.T) {
	words := taskWords("read a file to me")
	assert.True(t, words["read"])
	assert.True(t, words["file"])
	assert.False(t, words["a"], "one-letter words would match everything")
	assert.False(t, words["to"])
	assert.False(t, words["me"])
}

func TestRankToolsIsDeterministicOnTies(t *testing.T) {
	reg := registryWithTools(t, "b.read", "a.read", "c.read")
	ranked := rankTools(reg.ListTools(), "nothing matches", map[string]bool{})
	require.Len(t, ranked, 3)
	assert.Equal(t, "a.read", ranked[0].Name, "ties break by name")
	assert.Equal(t, "c.read", ranked[2].Name)
}

// usedToolNames reads the ledger, which is already the record of what ran.
func TestUsedToolNamesFromLedger(t *testing.T) {
	ledger := []core.ToolCallRecord{{Tool: "file.read"}, {Tool: "git.status"}, {Tool: ""}}
	used := usedToolNames(ledger)
	assert.True(t, used["file.read"])
	assert.True(t, used["git.status"])
	assert.Len(t, used, 2, "empty names are skipped")
}

// The rendered list stays parseable and names the requirement.
func TestRenderToolListShape(t *testing.T) {
	defs := []*core.ToolDef{{Name: "file.read", Description: "reads", RequiredParams: []string{"path"}}}
	out := renderToolList(defs, 0)
	assert.Contains(t, out, "Available tools:")
	assert.Contains(t, out, "file.read: reads")
	assert.Contains(t, out, "Required params: [path]")

	assert.Equal(t, "No tools available.", renderToolList(nil, 0))
}

var _ = json.Marshal
var _ = strings.TrimSpace
