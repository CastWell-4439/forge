// Package harness implements M2: Agent Harness — the ReAct execution loop
// that drives autonomous agent behavior through Think→Act→Observe cycles.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// LLMConfig holds configuration for the LLM API client.
type LLMConfig struct {
	BaseURL     string        // e.g. "https://bmc-llm-relay.bluemediagroup.cn/v1"
	APIKey      string
	Model       string        // e.g. "claude-opus-4-6-v1"
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration
}

// DefaultLLMConfig returns config with sensible defaults.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Model:       "claude-opus-4-6-v1",
		Temperature: 0.7,
		MaxTokens:   4096,
		Timeout:     60 * time.Second,
	}
}

// LLMClient implements core.LLMClient by calling an OpenAI-compatible API.
type LLMClient struct {
	config LLMConfig
	client *http.Client
}

// NewLLMClient creates a new LLM API client.
func NewLLMClient(config LLMConfig) *LLMClient {
	return &LLMClient{
		config: config,
		client: &http.Client{Timeout: config.Timeout},
	}
}

// chatRequest is the request body for the OpenAI-compatible chat API.
type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []chatMessage     `json:"messages"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the response from the OpenAI-compatible chat API.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat sends messages to the LLM and returns the response text.
// Implements core.LLMClient.
func (c *LLMClient) Chat(ctx context.Context, messages []core.Message) (string, error) {
	// Convert core.Message to chatMessage.
	chatMsgs := make([]chatMessage, len(messages))
	for i, m := range messages {
		chatMsgs[i] = chatMessage{Role: m.Role, Content: m.Content}
	}

	reqBody := chatRequest{
		Model:       c.config.Model,
		Messages:    chatMsgs,
		Temperature: c.config.Temperature,
		MaxTokens:   c.config.MaxTokens,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM API call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("LLM API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// Verify LLMClient implements core.LLMClient at compile time.
var _ core.LLMClient = (*LLMClient)(nil)
