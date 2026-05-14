// Package harness implements M2: Agent Harness — the ReAct execution loop
// that drives autonomous agent behavior through Think→Act→Observe cycles.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	MaxRetries  int           // max retry attempts on transient errors (default 3)
}

// DefaultLLMConfig returns config with sensible defaults.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Model:       "claude-opus-4-6-v1",
		Temperature: 0.7,
		MaxTokens:   4096,
		Timeout:     60 * time.Second,
		MaxRetries:  3,
	}
}

// LLMClient implements core.LLMClient by calling an OpenAI-compatible API.
// Includes exponential backoff retry for 429/5xx errors.
type LLMClient struct {
	config LLMConfig
	client *http.Client
}

// NewLLMClient creates a new LLM API client.
func NewLLMClient(config LLMConfig) *LLMClient {
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	return &LLMClient{
		config: config,
		client: &http.Client{Timeout: config.Timeout},
	}
}

// chatRequest is the request body for the OpenAI-compatible chat API.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
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
// Implements core.LLMClient.Chat.
func (c *LLMClient) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := c.ChatWithUsage(ctx, messages)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// ChatWithUsage sends messages and returns both the response and token usage.
// Implements core.LLMClient.ChatWithUsage.
// Retries on 429 (rate limit) and 5xx (server error) with exponential backoff.
func (c *LLMClient) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
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
		return core.ChatResult{}, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s...
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return core.ChatResult{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		result, err := c.doRequest(ctx, body)
		if err == nil {
			return result, nil
		}

		// Check if retryable.
		if isRetryable(err) {
			lastErr = err
			continue
		}
		return core.ChatResult{}, err
	}

	return core.ChatResult{}, fmt.Errorf("LLM API failed after %d retries: %w", c.config.MaxRetries, lastErr)
}

// doRequest performs a single HTTP request to the LLM API.
func (c *LLMClient) doRequest(ctx context.Context, body []byte) (core.ChatResult, error) {
	url := c.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return core.ChatResult{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return core.ChatResult{}, &retryableError{err: fmt.Errorf("LLM API call failed: %w", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.ChatResult{}, fmt.Errorf("read response: %w", err)
	}

	// Rate limited or server error → retryable.
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return core.ChatResult{}, &retryableError{
			err: fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return core.ChatResult{}, fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return core.ChatResult{}, fmt.Errorf("parse response: %w", err)
	}

	if chatResp.Error != nil {
		return core.ChatResult{}, fmt.Errorf("LLM API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return core.ChatResult{}, fmt.Errorf("LLM returned no choices")
	}

	return core.ChatResult{
		Content: chatResp.Choices[0].Message.Content,
		Usage: core.TokenUsage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		},
	}, nil
}

// --- Retry helpers ---

// retryableError marks an error as eligible for retry.
type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	_, ok := err.(*retryableError)
	return ok
}

// Verify LLMClient implements core.LLMClient at compile time.
var _ core.LLMClient = (*LLMClient)(nil)
