package harness

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// Relevant tool injection (C3).
//
// The prompt used to list every tool on every turn. That cost is paid on each
// request, and it grows with the registry rather than with the task: a run that
// only reads files still carried the description of every git, web, shell and
// MCP tool. The native path never had this problem — tools travel as an API
// parameter there — so this is about the prompt path, which is the one that
// runs when a provider does not support native tool calls.
//
// Two rules keep the reduction honest:
//
//  1. The visible set is chosen by relevance, and the cutoff is DISCOVERABLE:
//     when tools are hidden, the prompt says how many and how to find them
//     (tool.search). A silent cutoff would be an invisible capability loss.
//  2. Everything needed to ask, pause or manage the window stays visible
//     regardless of relevance — those are the tools a stuck run needs most.

// DefaultVisibleTools is how many tool descriptions the prompt carries when no
// limit is configured.
const DefaultVisibleTools = 12

// alwaysVisibleTools are injected whatever the relevance score says.
//
// The meta tools are the escape hatches: a run that cannot see agent.pause
// cannot ask for help, and one that cannot see tool.search cannot discover
// what it is missing. Their descriptions are short, so the cost of always
// including them is small and the cost of hiding them is a stuck run.
var alwaysVisibleTools = []string{
	ToolSearchToolName,
	PauseToolName,
	QueryToolName,
	"context.remaining",
	"context.compact",
	"context.recall",
}

// rankTools orders tools by how likely they are to matter for this task.
//
// Scoring is deliberately simple and deterministic:
//
//	3 points — the tool was already used in this run (it is what the run does)
//	2 points — a word of its name appears in the task text
//	1 point  — a word of its description appears in the task text
//	0        — nothing matched
//
// Ties break by name so the same run always produces the same prompt, which
// keeps the prompt prefix cacheable (the same reason compaction puts its
// summary in a fixed place).
func rankTools(defs []*core.ToolDef, taskText string, used map[string]bool) []*core.ToolDef {
	words := taskWords(taskText)

	type scored struct {
		def   *core.ToolDef
		score int
	}
	ranked := make([]scored, 0, len(defs))
	for _, def := range defs {
		score := 0
		if used[def.Name] {
			score += 3
		}
		name := strings.ToLower(def.Name)
		desc := strings.ToLower(def.Description)
		// Word-boundary matching, not substring: "text" appears inside
		// "conTEXT.compact", and a naive Contains would score the compaction
		// tool for every task that mentions text.
		for word := range words {
			if containsWord(name, word) {
				score += 2
				break
			}
		}
		for word := range words {
			if containsWord(desc, word) {
				score++
				break
			}
		}
		ranked = append(ranked, scored{def: def, score: score})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].def.Name < ranked[j].def.Name
	})

	out := make([]*core.ToolDef, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.def)
	}
	return out
}

// containsWord reports whether haystack contains needle as a whole word.
//
// The tool-name alphabet is [a-z0-9._-], so a word boundary here means "the
// character before/after is not one of those". This is what stops "text" from
// matching "context.compact" — a substring test would score the wrong tools
// for a large fraction of ordinary tasks.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	isNameChar := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
	}
	from := 0
	for {
		idx := strings.Index(haystack[from:], needle)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(needle)
		leftOK := start == 0 || !isNameChar(haystack[start-1])
		rightOK := end == len(haystack) || !isNameChar(haystack[end])
		if leftOK && rightOK {
			return true
		}
		from = start + 1
		if from >= len(haystack) {
			return false
		}
	}
}

// taskWords extracts comparable words from the task text and the recent
// conversation. Words shorter than three characters are dropped: matching on
// "a" or "to" would make every tool relevant.
func taskWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-'
	}) {
		if len(field) >= 3 {
			out[field] = true
		}
	}
	return out
}

// selectVisibleTools returns the tool descriptions the prompt should carry,
// plus how many were left out.
func (l *AgentLoop) selectVisibleTools(taskText string, used map[string]bool) ([]*core.ToolDef, int) {
	if l.router == nil || l.router.registry == nil {
		return nil, 0
	}
	all := l.router.registry.ListTools()
	limit := l.config.VisibleTools
	if limit == 0 {
		limit = DefaultVisibleTools
	}
	if limit < 0 || len(all) <= limit {
		// Negative disables filtering; a registry that fits needs no cutoff.
		return all, 0
	}

	ranked := rankTools(all, taskText, used)

	chosen := make([]*core.ToolDef, 0, limit)
	seen := map[string]bool{}
	for _, name := range alwaysVisibleTools {
		for _, def := range ranked {
			if def.Name == name && !seen[name] {
				chosen = append(chosen, def)
				seen[name] = true
				break
			}
		}
	}
	for _, def := range ranked {
		if len(chosen) >= limit {
			break
		}
		if seen[def.Name] {
			continue
		}
		chosen = append(chosen, def)
		seen[def.Name] = true
	}

	// Deterministic order for the prompt (cacheable prefix): sort by name, so
	// the same set always renders the same way regardless of ranking ties.
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Name < chosen[j].Name })
	return chosen, len(all) - len(chosen)
}

// renderToolList formats the visible tools, and — when some are hidden — says
// how many and how to reach them.
//
// The hidden line is not decoration: without it the model cannot tell a tool it
// does not have from one it simply cannot see, and would either give up or
// invent a call.
func renderToolList(defs []*core.ToolDef, hidden int) string {
	if len(defs) == 0 {
		return "No tools available."
	}
	var b strings.Builder
	b.WriteString("Available tools:\n")
	for _, def := range defs {
		fmt.Fprintf(&b, "- %s: %s\n", def.Name, def.Description)
		if len(def.RequiredParams) > 0 {
			fmt.Fprintf(&b, "  Required params: %v\n", def.RequiredParams)
		}
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "\n%d more tool(s) are not listed here. "+
			"Call %s with a keyword to find the one you need.\n", hidden, ToolSearchToolName)
	}
	return b.String()
}

// runToolSearch answers a tool.search call by looking the registry up.
func (l *AgentLoop) runToolSearch(params map[string]any) *core.ToolResult {
	query, _ := params["query"].(string)
	if strings.TrimSpace(query) == "" {
		return &core.ToolResult{Error: fmt.Sprintf("%s: missing required param 'query'", ToolSearchToolName)}
	}
	limit := toInt(params["limit"])

	matches := workers.SearchTools(l.router.registry, query, limit)
	status := "ok"
	if len(matches) == 0 {
		status = "no_match"
	}
	payload, err := json.Marshal(map[string]any{"matches": matches, "status": status})
	if err != nil {
		return &core.ToolResult{Error: fmt.Sprintf("%s: encode result: %v", ToolSearchToolName, err)}
	}
	return &core.ToolResult{Output: string(payload)}
}

// usedToolNames collects the tools this run has already called, so the ranking
// can favour them. Read from the ledger rather than tracked separately: the
// ledger is already the record of what ran.
func usedToolNames(ledger []core.ToolCallRecord) map[string]bool {
	used := make(map[string]bool, len(ledger))
	for _, rec := range ledger {
		if rec.Tool != "" {
			used[rec.Tool] = true
		}
	}
	return used
}

// taskTextForPrompt is what relevance is judged against: the user's request
// plus the recent conversation, so a run that has moved on to a second phase
// gets the tools for that phase rather than only for its opening line.
func taskTextForPrompt(messages []core.Message) string {
	var b strings.Builder
	// The tail is what matters; the whole history would keep every earlier
	// phase's vocabulary relevant forever.
	start := 0
	if len(messages) > 6 {
		start = len(messages) - 6
	}
	for _, m := range messages[start:] {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}
