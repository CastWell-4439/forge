package harness

import (
	"context"
	"fmt"
	"log"
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
)

// ContextManager manages the conversation history for the ReAct loop.
// When the history exceeds the token budget, it compresses old messages
// into a summary to prevent context window overflow.
type ContextManager struct {
	maxTokens int
	llm       core.LLMClient
}

// NewContextManager creates a new ContextManager.
func NewContextManager(maxTokens int, llm core.LLMClient) *ContextManager {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxContextTokens
	}
	return &ContextManager{
		maxTokens: maxTokens,
		llm:       llm,
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

// CompactIfNeeded checks if the messages exceed the token budget.
// If so, it compresses the oldest non-system messages into a summary,
// keeping the system prompt and recent messages intact.
//
// After compression, performs a second pass to truncate any oversized
// tool results that still push us over budget.
//
// Returns the (possibly compacted) message list.
func (cm *ContextManager) CompactIfNeeded(ctx context.Context, messages []core.Message) ([]core.Message, error) {
	tokens := EstimateTokens(messages)
	if tokens <= cm.maxTokens {
		return messages, nil
	}

	// Separate system prompt from conversation.
	var systemMsgs []core.Message
	var convMsgs []core.Message

	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			convMsgs = append(convMsgs, m)
		}
	}

	if len(convMsgs) <= 2 {
		// Can't compress further — just the last exchange.
		return messages, nil
	}

	// Keep the last 4 messages (2 exchanges), summarize the rest.
	keepCount := 4
	if keepCount > len(convMsgs) {
		keepCount = len(convMsgs)
	}

	toSummarize := convMsgs[:len(convMsgs)-keepCount]
	toKeep := convMsgs[len(convMsgs)-keepCount:]

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

	// Build new message list: system + summary + recent messages.
	result := make([]core.Message, 0, len(systemMsgs)+1+len(toKeep))
	result = append(result, systemMsgs...)
	result = append(result, core.Message{
		Role:    "system",
		Content: fmt.Sprintf("[Conversation summary: %s]", summary),
	})
	result = append(result, toKeep...)

	// Second pass: truncate oversized tool results if still over budget.
	result = cm.truncateToolResults(result)

	return result, nil
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
