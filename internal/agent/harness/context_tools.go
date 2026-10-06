package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// Context-window operations (N2c).
//
// The loop owns three meta tools — remaining / compact / recall — and
// intercepts them before the router (see loop.go). Two properties matter:
//
//  1. They act on the loop's OWN state, not on the world. Compacting must
//     never look like "start over": the environment, the side-effect ledger
//     and world state are untouched, and the tool description says so, because
//     a model that reads a new window as a fresh start will redo work.
//  2. Compaction is never destructive. Every message that leaves the working
//     set is archived first — into the journal when one is configured (the
//     D-12 source of truth) and always into a bounded in-memory archive, so
//     context.recall has something to search even without a journal.
//     "Shrink the window" and "throw the history away" are different
//     operations, and only the first one is allowed here.

// ContextArchive is the in-memory record of messages that left the window.
// Bounded: a long run must not grow it without limit.
type ContextArchive struct {
	mu       sync.Mutex
	entries  []archivedMessage
	maxItems int
}

// archivedMessage is one archived message with where it came from.
type archivedMessage struct {
	Step    int
	Role    string
	Content string
}

// DefaultArchiveLimit bounds the in-memory archive by message count.
const DefaultArchiveLimit = 500

// NewContextArchive creates an archive with the given item cap (0 = default).
func NewContextArchive(maxItems int) *ContextArchive {
	if maxItems <= 0 {
		maxItems = DefaultArchiveLimit
	}
	return &ContextArchive{maxItems: maxItems}
}

// Add archives messages, dropping the oldest when over the cap.
func (a *ContextArchive) Add(step int, messages []core.Message) {
	if a == nil || len(messages) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range messages {
		a.entries = append(a.entries, archivedMessage{Step: step, Role: m.Role, Content: m.Content})
	}
	if len(a.entries) > a.maxItems {
		a.entries = a.entries[len(a.entries)-a.maxItems:]
	}
}

// Len reports how many messages are archived.
func (a *ContextArchive) Len() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// RecallResult is one excerpt from the archive.
type RecallResult struct {
	Step    int    `json:"step"`
	Role    string `json:"role"`
	Excerpt string `json:"excerpt"`
}

// Recall searches the archive case-insensitively. The status distinguishes the
// ways a search can come back empty, so the model never reads "no match in
// what we kept" as "this never happened":
//
//	ok       — matches found
//	no_match — the archive exists and genuinely has no match
//	no_archive — nothing has been archived yet (no compaction has run)
func (a *ContextArchive) Recall(_ context.Context, query string, limit int) ([]RecallResult, string) {
	if a == nil || a.Len() == 0 {
		return nil, "no_archive"
	}
	if limit <= 0 {
		limit = 5
	}
	needle := strings.ToLower(strings.TrimSpace(query))

	a.mu.Lock()
	defer a.mu.Unlock()
	var out []RecallResult
	for _, e := range a.entries {
		if needle == "" || strings.Contains(strings.ToLower(e.Content), needle) {
			out = append(out, RecallResult{Step: e.Step, Role: e.Role, Excerpt: excerpt(e.Content, needle)})
			if len(out) >= limit {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, "no_match"
	}
	return out, "ok"
}

// excerpt clips a message around the match, so a recall answer points into
// the archived message instead of dumping it whole.
func excerpt(content, needle string) string {
	const radius = 200
	idx := 0
	if needle != "" {
		if found := strings.Index(strings.ToLower(content), needle); found >= 0 {
			idx = found
		}
	}
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + radius
	if end > len(content) {
		end = len(content)
	}
	out := content[start:end]
	if start > 0 {
		out = "..." + out
	}
	if end < len(content) {
		out += "..."
	}
	return out
}

// contextUsage describes where the window stands.
type contextUsage struct {
	Used     int
	Max      int
	Left     int
	Fraction float64
	Known    bool
}

// usage computes the current window usage. Known is false when no budget is
// configured — the caller must then report null rather than invent a number
// (Codex's get_context_remaining returns null for the same reason).
func (l *AgentLoop) usage(messages []core.Message) contextUsage {
	maxTokens := l.ctxMgr.maxTokens
	if maxTokens <= 0 {
		return contextUsage{}
	}
	used := EstimateTokens(messages)
	left := maxTokens - used
	if left < 0 {
		left = 0
	}
	return contextUsage{
		Used:     used,
		Max:      maxTokens,
		Left:     left,
		Fraction: float64(used) / float64(maxTokens),
		Known:    true,
	}
}

// isContextTool reports whether a tool name is one the loop intercepts.
func isContextTool(name string) bool {
	switch name {
	case "context.remaining", "context.compact", "context.recall":
		return true
	default:
		return false
	}
}

// runContextTool executes one intercepted context operation. A compaction
// mutates *messages wholesale; the other two are read-only. It never enters
// the side-effect ledger: these operations touch the loop's own state, not
// the world, so idempotency accounting does not apply.
func (l *AgentLoop) runContextTool(ctx context.Context, name string, params map[string]any,
	messages *[]core.Message, sessionID string, step int) *core.ToolResult {
	switch name {
	case "context.remaining":
		usage := l.usage(*messages)
		var payload []byte
		if usage.Known {
			payload, _ = json.Marshal(map[string]any{
				"tokens_left": usage.Left,
				"tokens_used": usage.Used,
				"max_tokens":  usage.Max,
			})
		} else {
			// No budget configured: null, not a fabricated figure.
			payload, _ = json.Marshal(map[string]any{
				"tokens_left": nil,
				"tokens_used": nil,
				"max_tokens":  nil,
				"note":        "token accounting is unavailable: this loop has no context budget configured",
			})
		}
		return &core.ToolResult{Output: string(payload)}

	case "context.compact":
		oldCount := len(*messages)
		summary, compacted, err := l.compactNow(ctx, messages, sessionID, step)
		if err != nil {
			return &core.ToolResult{Error: fmt.Sprintf("context.compact: %v", err)}
		}
		status := "noop"
		if compacted {
			status = "compacted"
		}
		payload, _ := json.Marshal(map[string]any{
			"status":           status,
			"tokens_used":      EstimateTokens(*messages),
			"messages_removed": oldCount - len(*messages),
			"summary":          summary,
			"note":             "environment state, side effects and the tool ledger are unchanged; archived messages stay searchable with context.recall",
		})
		return &core.ToolResult{Output: string(payload)}

	case "context.recall":
		query, _ := params["query"].(string)
		if strings.TrimSpace(query) == "" {
			return &core.ToolResult{Error: "context.recall: missing required param 'query'"}
		}
		matches, status := l.archive.Recall(ctx, query, toInt(params["limit"]))
		payload, _ := json.Marshal(map[string]any{"matches": matches, "status": status})
		return &core.ToolResult{Output: string(payload)}

	default:
		return &core.ToolResult{Error: fmt.Sprintf("unknown context tool %q", name)}
	}
}

// toInt reads a numeric parameter after a JSON round trip (float64) or from a
// directly constructed map (int). Anything else is zero, which callers treat
// as "unset".
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// compactNow summarises and replaces the conversation. Order matters:
//
//	summarise first — if that fails nothing has been archived or journaled,
//	so the journal never claims a compaction that did not happen;
//	archive + journal the removal — the moment the text leaves the window;
//	rebuild the list — summary goes at the END of the system block, a stable
//	position that keeps the prompt prefix cacheable (D-30 S3).
//
// compacted reports whether *messages was replaced, so the caller can reset
// its journal delta base before appending this step's observation.
func (l *AgentLoop) compactNow(ctx context.Context, messages *[]core.Message, sessionID string, step int) (summary string, compacted bool, err error) {
	original := *messages
	if len(original) <= 2 {
		return "", false, fmt.Errorf("nothing to compact: the conversation is already minimal")
	}

	// Split the system prompt(s) off, same split CompactIfNeeded uses, so the
	// static and model-initiated paths agree on what "history" is.
	var systemMsgs, convMsgs []core.Message
	for _, m := range original {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			convMsgs = append(convMsgs, m)
		}
	}
	keep := l.compactKeepMessages()
	if len(convMsgs) <= keep {
		return "", false, fmt.Errorf("nothing to compact: only %d conversation messages remain", len(convMsgs))
	}
	toSummarize := convMsgs[:len(convMsgs)-keep]
	toKeep := convMsgs[len(convMsgs)-keep:]

	summary, err = l.ctxMgr.summarize(ctx, toSummarize)
	if err != nil {
		// The archive is untouched: nothing archived, nothing journaled, the
		// window unchanged. The error names the summariser.
		return "", false, fmt.Errorf("summarise: %w", err)
	}

	// Archive BEFORE shrinking: compaction must never be the step that loses
	// text. The journal event carries the archived messages (durable half);
	// the in-memory archive is what context.recall searches.
	l.archive.Add(step, toSummarize)
	if l.journal != nil && sessionID != "" {
		if jerr := l.journalAppend(ctx, RunEvent{
			RunID: sessionID,
			Type:  EventContextCompacted,
			Step:  step,
			TS:    time.Now().UTC(),
			Data: map[string]any{
				// The post-compaction working set (rebuild treats this as a
				// replace), plus the messages that left it so nothing lives
				// only in memory.
				"messages": buildCompactSnapshot(systemMsgs, summary, toKeep),
				"archived": toSummarize,
			},
		}); jerr != nil {
			// A journal that cannot record the removal must not receive one
			// silently: recall would then search only the memory archive and
			// the rebuild would replay a stale full history. Failing here
			// leaves the window untouched.
			return "", false, fmt.Errorf("journal the compaction: %w", jerr)
		}
	}

	*messages = buildCompactSnapshot(systemMsgs, summary, toKeep)
	// Second pass: oversized tool results are the cheapest thing to shrink.
	*messages = l.ctxMgr.truncateToolResults(*messages)
	return summary, true, nil
}

// buildCompactSnapshot assembles the post-compaction list: system prompt(s),
// the summary in its stable position, the kept tail.
func buildCompactSnapshot(systemMsgs []core.Message, summary string, toKeep []core.Message) []core.Message {
	result := make([]core.Message, 0, len(systemMsgs)+1+len(toKeep))
	result = append(result, systemMsgs...)
	result = append(result, core.Message{
		Role:    "system",
		Content: fmt.Sprintf("[Conversation summary: %s]", summary),
	})
	result = append(result, toKeep...)
	return result
}

// compactKeepMessages resolves how many trailing messages stay un-summarised.
func (l *AgentLoop) compactKeepMessages() int {
	if l.config.CompactKeepMessages > 0 {
		return l.config.CompactKeepMessages
	}
	return defaultCompactKeepMessages
}

// defaultCompactKeepMessages keeps the last two exchanges (four messages).
const defaultCompactKeepMessages = 4

// contextReminderLine renders the water-line reminder injected when usage
// crosses a threshold. It reports a RELATIVE figure ("over 70%"), not exact
// tokens: the exact number is one context.remaining call away, and printing
// it every step would cost more than it informs.
func contextReminderLine(usage contextUsage, level string) string {
	if level == "critical" {
		return fmt.Sprintf(
			"[context: %.0f%% of the window is used (~%d tokens left). "+
				"Call context.compact to summarise and archive the conversation, or context.remaining for exact figures.]",
			usage.Fraction*100, usage.Left)
	}
	return fmt.Sprintf(
		"[context: %.0f%% of the window is used (~%d tokens left). "+
			"context.compact can summarise the conversation when you no longer need the earlier turns.]",
		usage.Fraction*100, usage.Left)
}

// reminderLevel returns which (if any) reminder is due, and marks it as sent
// so each threshold fires at most once per run — crossing 70% on step 4 must
// not re-announce itself on steps 5 through 40.
func (l *AgentLoop) reminderLevel(usage contextUsage) string {
	if !usage.Known || l.config.ContextRemindAt < 0 {
		return ""
	}
	warn, critical := l.reminderThresholds()
	switch {
	case usage.Fraction >= critical:
		if l.remindedCritical {
			return ""
		}
		l.remindedCritical = true
		l.remindedWarn = true // a critical sighting covers the warning
		return "critical"
	case usage.Fraction >= warn:
		if l.remindedWarn {
			return ""
		}
		l.remindedWarn = true
		return "warn"
	}
	return ""
}

// reminderThresholds resolves the two water lines (fractions of the budget).
func (l *AgentLoop) reminderThresholds() (warn, critical float64) {
	warn, critical = 0.70, 0.90
	if l.config.ContextRemindAt > 0 {
		warn = l.config.ContextRemindAt
	}
	if l.config.ContextUrgentAt > 0 {
		critical = l.config.ContextUrgentAt
	}
	if critical < warn {
		critical = warn
	}
	return warn, critical
}
