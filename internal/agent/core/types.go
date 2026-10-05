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
	// FinishReason is the provider's stop reason, e.g. "stop" or "length".
	// Callers must check it: "length" means the response was cut off by the
	// token limit and its content is incomplete.
	FinishReason string
	// ToolCalls carries the provider's native function-calling requests when
	// the client was asked with tools (ChatWithTools). Empty on the prompt
	// path — there the model's JSON lives in Content instead.
	ToolCalls []NativeToolCall
}

// NativeToolCall is one function-calling request as the provider returned it.
type NativeToolCall struct {
	// ID correlates the call with its result message in the history.
	ID string `json:"id"`
	// Name is the tool name; Arguments are the parsed parameter object.
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
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
//
// ToolCalls and ToolCallID are the native function-calling history: an
// assistant message may carry requests, and a "tool" message answers one of
// them by ID. They are empty on the prompt path, which keeps its plain
// user/assistant text history unchanged.
type Message struct {
	Role       string           `json:"role"` // "user" | "assistant" | "system" | "tool"
	Content    string           `json:"content"`
	ToolCalls  []NativeToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
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
