package harness

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/castwell/forge/internal/agent/core"
)

const (
	// DefaultMaxContextTokens is the approximate token limit before compression.
	// Conservative estimate: 1 token ≈ 4 chars for English, ≈ 2 chars for Chinese.
	DefaultMaxContextTokens = 100000

	// charsPerToken is the approximate characters per token for estimation.
	charsPerToken = 3

	// maxToolResultChars is the max length of a single tool result before truncation.
	maxToolResultChars = 8000

	// defaultCompactTarget is where a compaction aims (S4): shrink to this
	// fraction of the budget instead of down to the bare keep-tail, so the
	// newest context survives with room for the next steps. Configurable via
	// LoopConfig.ContextCompactTarget / FORGE_CONTEXT_COMPACT_TARGET.
	defaultCompactTarget = 0.60

	// summaryEstimateTokens budgets for the summary that a compaction will
	// insert, so "does this fit the target" can be answered BEFORE paying the
	// summariser. A rough constant on purpose — the real summary is one call
	// away and the estimate only steers how much gets dropped.
	summaryEstimateTokens = 250

	// Layered eviction (S2): oversized TOOL observations are slimmed first —
	// they are the heaviest and most rebuildable content — and only what still
	// does not fit gets summarised. Ordinary user text is never slimmed.
	toolSlimThresholdChars = 2000
	toolSlimHeadChars      = 1500
	toolSlimTailChars      = 500

	// Calibration bounds (N3). The ratio observed/estimated is clamped to this
	// range: one anomalous response must not be able to move the estimate by an
	// order of magnitude, and a provider that reports something wildly different
	// from the text length is a reason to distrust the sample, not to trust it
	// completely.
	minCalibrationRatio = 0.5
	maxCalibrationRatio = 3.0

	// calibrationSamples is how many observations are needed before the
	// estimate is adjusted at all. Below it the default heuristic stands: two
	// samples are a coincidence, not a ratio.
	calibrationSamples = 3

	// calibrationSmoothing weights a new observation against the running ratio.
	// 0.3 keeps the value responsive without letting one sample dominate.
	calibrationSmoothing = 0.3
)

// ContextManager manages the conversation history for the ReAct loop.
// When the history exceeds the token budget, it compresses old messages
// into a summary to prevent context window overflow.
type ContextManager struct {
	maxTokens int
	llm       core.LLMClient
	// keep is how many trailing conversation messages survive a compaction
	// un-summarised (S1: configurable; 0 in config means defaultCompactKeepMessages).
	keep int
	// compactTarget is the fraction of the budget a compaction aims to reach
	// (S4); <=0 in config means defaultCompactTarget.
	compactTarget float64
	// reserveOutput is how many tokens to hold back for the model's own reply
	// (N3). The budget covers the whole window, so a generation that is allowed
	// to run to MaxTokens would otherwise push the request past it.
	reserveOutput int
	// reserveWarned makes the "unaffordable reservation" warning fire once, so
	// the log does not fill on every budget query.
	reserveWarned bool
	// calibration is the observed/estimated ratio learned from real usage
	// reports (N3), with its sample count. Zero samples means "not calibrated
	// yet" and the default heuristic stands.
	calibrationRatio   float64
	calibrationSamples int
}

// NewContextManager creates a new ContextManager.
func NewContextManager(maxTokens int, llm core.LLMClient) *ContextManager {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxContextTokens
	}
	return &ContextManager{
		maxTokens:     maxTokens,
		llm:           llm,
		keep:          defaultCompactKeepMessages,
		compactTarget: defaultCompactTarget,
	}
}

// EstimateTokens estimates the token count of a message list.
// This is a rough heuristic — production systems would use a tokenizer.
func EstimateTokens(messages []core.Message) int {
	total := 0
	for _, m := range messages {
		// Each message has overhead (~4 tokens for role/formatting).
		total += 4 + len(m.Content)/charsPerToken
	}
	return total
}

// CompactIfNeeded checks if the messages exceed the token budget and
// compacts in layers (S1–S4, the static half of the dual-track design):
//
//  1. Evict the cheap way first (S2): oversized TOOL observations are slimmed
//     to head+tail — they are the heaviest and most rebuildable content.
//     Ordinary user text is untouched. Often this alone gets back under budget
//     and nothing is summarised away.
//  2. Plan what to summarise (S1+S4): keep the configured tail (default 4,
//     widened so the last tool observation always survives), then summarise
//     the oldest messages only as far as needed to reach the target fraction
//     of the budget (default 60%) — recent turns that already fit are left
//     alone instead of being thrown into the summary with the rest.
//  3. Summarise the planned range; on failure drop it (the honest fallback,
//     logged) and keep everything else.
//
// Returns the (possibly unchanged) message list.
func (cm *ContextManager) CompactIfNeeded(ctx context.Context, messages []core.Message) ([]core.Message, error) {
	// Layer 1: slim heavy tool output before any decision about summarising.
	slimmed := cm.slimToolObservations(messages)
	// The budget compared against is the INPUT budget (N3): the window minus
	// the reply reservation, so a request that fits does not then overflow when
	// the model generates up to MaxTokens.
	if cm.estimateTokens(slimmed) <= cm.inputBudget() {
		return slimmed, nil
	}

	// Separate system prompt from conversation.
	var systemMsgs []core.Message
	var convMsgs []core.Message
	for _, m := range slimmed {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			convMsgs = append(convMsgs, m)
		}
	}
	if len(convMsgs) <= 2 {
		// Can't compress further — just the last exchange.
		return slimmed, nil
	}

	toSummarize, toKeep := cm.planCompaction(systemMsgs, convMsgs, false)
	if len(toSummarize) == 0 {
		// Nothing may be summarised (keep-tail floor swallowed the history, or
		// the tail alone already fits the target). Truncation pass still runs —
		// and the summariser is NOT called with an empty range, which the old
		// code did when conv == keepCount.
		return cm.truncateToolResults(slimmed), nil
	}

	summary, err := cm.summarize(ctx, toSummarize)
	if err != nil {
		// Summarization failed, so dropping the old turns is the only way back
		// under budget — but it must not happen silently: the model loses that
		// context for the rest of the run. Build a fresh slice rather than
		// appending onto systemMsgs, which would share its backing array.
		log.Printf("[harness] context summarization failed, dropping %d old messages: %v",
			len(toSummarize), err)
		result := make([]core.Message, 0, len(systemMsgs)+len(toKeep))
		result = append(result, systemMsgs...)
		result = append(result, toKeep...)
		return cm.truncateToolResults(result), nil
	}

	// Build new message list: system + summary + recent messages. The summary
	// goes at the END of the system block — a stable position that keeps the
	// prompt prefix cacheable (S3).
	result := make([]core.Message, 0, len(systemMsgs)+1+len(toKeep))
	result = append(result, systemMsgs...)
	result = append(result, core.Message{
		Role:    "system",
		Content: fmt.Sprintf("[Conversation summary: %s]", summary),
	})
	result = append(result, toKeep...)

	// Final pass: truncate oversized content if still over budget.
	result = cm.truncateToolResults(result)

	return result, nil
}

// planCompaction decides which messages leave the window and which stay,
// shared by the static path and the model's context.compact so the two can
// never drift apart (that shared-ness is the claim D-31 first made too early
// — now it is real, and pinned by tests).
//
// Rules, in order:
//   - keep = cm.keep trailing messages survive (S1);
//   - the floor: if the LAST tool observation lies outside the keep-tail, the
//     tail widens to include it — the model must not lose sight of the result
//     it most recently acted on;
//   - target: unless explicit is set, summarise the OLDEST messages only until
//     the projected result fits cm.compactTarget of the budget (S4), so recent
//     turns that already fit survive; an explicit request drops the whole
//     candidate range (the caller asked for a compaction, not a trim).
//
// Returns (nil, conv) when nothing may be summarised.
func (cm *ContextManager) planCompaction(systemMsgs, convMsgs []core.Message, explicit bool) (toSummarize, toKeep []core.Message) {
	keep := cm.keep
	if keep <= 0 {
		keep = defaultCompactKeepMessages
	}
	tailStart := len(convMsgs) - keep
	if tailStart < 0 {
		tailStart = 0
	}
	// S1 floor: widen the tail over the last tool observation.
	if last := lastToolObservationIndex(convMsgs); last >= 0 && last < tailStart {
		tailStart = last
	}
	candidates := convMsgs[:tailStart]
	if len(candidates) == 0 {
		return nil, convMsgs
	}

	targetTokens := 0
	if !explicit && cm.compactTarget > 0 {
		// The target is a fraction of the INPUT budget, not of the raw window:
		// compacting to 60% of a window that also has to hold the reply would
		// leave the reply unaccounted for (N3).
		targetTokens = int(float64(cm.inputBudget()) * cm.compactTarget)
	}
	if targetTokens <= 0 {
		// Explicit request (or no target configured): drop the whole range.
		return candidates, convMsgs[len(candidates):]
	}

	// Minimal drop that reaches the target: each extra message summarised
	// costs future context, so summarise only as far as needed (S4).
	systemTokens := cm.estimateTokens(systemMsgs)
	for drop := 1; drop <= len(candidates); drop++ {
		projected := systemTokens + cm.estimateTokens(convMsgs[drop:]) + summaryEstimateTokens
		if projected <= targetTokens {
			return convMsgs[:drop], convMsgs[drop:]
		}
	}
	return candidates, convMsgs[len(candidates):]
}

// isToolObservation reports whether a message is a tool observation the loop
// appended — the only content S2 is allowed to slim. Two shapes exist: the
// native path uses role "tool"; the prompt path echoes the same content as a
// user message starting with formatObservation's prefix.
func isToolObservation(m core.Message) bool {
	return m.Role == "tool" || strings.HasPrefix(m.Content, "[Tool ")
}

// lastToolObservationIndex returns the index of the newest tool observation
// in the conversation, or -1 when there is none.
func lastToolObservationIndex(conv []core.Message) int {
	for i := len(conv) - 1; i >= 0; i-- {
		if isToolObservation(conv[i]) {
			return i
		}
	}
	return -1
}

// slimToolObservations returns a copy where oversized tool observations are
// cut to head+tail with an honest marker. Non-tool content is never touched:
// a user's long message is not ours to edit.
func (cm *ContextManager) slimToolObservations(messages []core.Message) []core.Message {
	changed := false
	out := make([]core.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].Content) <= toolSlimThresholdChars || !isToolObservation(out[i]) {
			continue
		}
		orig := len(out[i].Content)
		head := truncateAtRuneBoundary(out[i].Content, toolSlimHeadChars)
		tail := tailRuneBoundary(out[i].Content, toolSlimTailChars)
		out[i].Content = head +
			fmt.Sprintf("\n\n[...slimmed for the context budget: original %d chars, kept head and tail. Ask for specific sections if needed.]\n\n", orig) +
			tail
		changed = true
	}
	if !changed {
		return messages
	}
	return out
}

// tailRuneBoundary takes the last n bytes of s, adjusted forward to a rune
// boundary so a multi-byte character is never split.
func tailRuneBoundary(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// truncateToolResults returns a copy of messages with any oversized content
// truncated. It never mutates the caller's slice: CompactIfNeeded may be
// handed a slice the caller still holds.
func (cm *ContextManager) truncateToolResults(messages []core.Message) []core.Message {
	out := make([]core.Message, len(messages))
	copy(out, messages)

	for i := range out {
		origLen := len(out[i].Content)
		if origLen > maxToolResultChars {
			out[i].Content = truncateAtRuneBoundary(out[i].Content, maxToolResultChars) +
				fmt.Sprintf(
					"\n\n[...truncated, original was %d chars. Ask for specific sections if needed.]",
					origLen,
				)
		}
	}
	return out
}

// truncateAtRuneBoundary cuts s to at most limit bytes without splitting a
// UTF-8 sequence, which a plain byte slice would do for non-ASCII content.
func truncateAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// summarize asks the LLM to compress a series of messages into a brief summary.
func (cm *ContextManager) summarize(ctx context.Context, messages []core.Message) (string, error) {
	// Build a text representation of the messages to summarize.
	var text string
	for _, m := range messages {
		text += fmt.Sprintf("[%s]: %s\n", m.Role, m.Content)
	}

	summaryMessages := []core.Message{
		{
			Role: "system",
			Content: "Summarize the following conversation in 2-3 sentences. " +
				"Focus on: what tools were called, what results were obtained, " +
				"and any decisions made. Be concise.",
		},
		{Role: "user", Content: text},
	}

	return cm.llm.Chat(ctx, summaryMessages)
}
