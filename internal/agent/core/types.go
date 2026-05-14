// Package core defines shared types and interfaces for the Agent layer.
// This package has zero internal dependencies — all other agent sub-packages
// depend on core, but core depends on nothing inside internal/agent/.
package core

import (
	"context"
)

// TokenUsage tracks token consumption from an LLM call.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResult holds the full response from a Chat call, including usage stats.
type ChatResult struct {
	Content string
	Usage   TokenUsage
}

// LLMClient is the interface for communicating with a Large Language Model.
// Implementations can be real API clients or mock clients for testing.
type LLMClient interface {
	// Chat sends messages to the LLM and returns the response text.
	Chat(ctx context.Context, messages []Message) (string, error)
	// ChatWithUsage is like Chat but also returns token usage statistics.
	// If not supported, returns zero usage.
	ChatWithUsage(ctx context.Context, messages []Message) (ChatResult, error)
}

// Message represents a single message in an LLM conversation.
type Message struct {
	Role    string `json:"role"`    // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// ToolCall represents a tool invocation request from the Agent.
type ToolCall struct {
	Name   string `json:"name"`
	Params string `json:"params"` // raw JSON
}

// ToolResult represents the result of a tool invocation.
type ToolResult struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}
