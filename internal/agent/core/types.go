// Package core defines shared types and interfaces for the Agent layer.
// This package has zero internal dependencies — all other agent sub-packages
// depend on core, but core depends on nothing inside internal/agent/.
package core

import (
	"context"
)

// LLMClient is the interface for communicating with a Large Language Model.
// Implementations can be real API clients or mock clients for testing.
type LLMClient interface {
	// Chat sends messages to the LLM and returns the response text.
	Chat(ctx context.Context, messages []Message) (string, error)
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
