package harness

import (
	"context"
	"fmt"

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
		// If summarization fails, just truncate.
		result := append(systemMsgs, toKeep...)
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

// truncateToolResults scans messages and truncates any tool output that
// exceeds maxToolResultChars. This prevents a single large tool result
// from blowing the context window even after compression.
func (cm *ContextManager) truncateToolResults(messages []core.Message) []core.Message {
	for i := range messages {
		if len(messages[i].Content) > maxToolResultChars {
			truncated := messages[i].Content[:maxToolResultChars]
			messages[i].Content = truncated + fmt.Sprintf(
				"\n\n[...truncated, original was %d chars. Ask for specific sections if needed.]",
				len(messages[i].Content)+len(truncated),
			)
		}
	}
	return messages
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
