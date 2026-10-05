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
	"strings"
	"sync/atomic"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// LLMConfig holds configuration for the LLM API client.
type LLMConfig struct {
	BaseURL     string // e.g. "https://api.example.com/v1"
	APIKey      string
	Model       string // e.g. "claude-opus-4-6-v1"
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration
	MaxRetries  int // max retry attempts on transient errors (default 3)
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
	// toolsBroken latches once the endpoint refuses the "tools" field: the
	// capability is a property of the endpoint, not of one request, so every
	// later call skips the attempt and reports ErrToolsUnsupported directly.
	toolsBroken atomic.Bool
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
	// Tools is the native function-calling list (absent on the prompt path).
	Tools []openAITool `json:"tools,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Native history: an assistant message carries its tool_calls; a tool
	// message answers one call by ID.
	ToolCalls  []core.NativeToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
}

// wireToolCall is the response-side shape of one tool call.
type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatResponse is the response from the OpenAI-compatible chat API.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
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
	return c.retrying(ctx, c.buildRequest(messages, nil), false)
}

// ChatWithTools sends messages plus the tool definitions and parses native
// tool_calls. It implements ToolAwareLLM.
//
// The first endpoint refusal to accept "tools" latches toolsBroken on this
// client and returns ErrToolsUnsupported, so the loop degrades to the prompt
// path for this step and every later one — one failed attempt, one warning,
// no repeated doomed requests.
func (c *LLMClient) ChatWithTools(ctx context.Context, messages []core.Message, defs []*core.ToolDef) (core.ChatResult, error) {
	if c.toolsBroken.Load() {
		return core.ChatResult{}, ErrToolsUnsupported
	}
	return c.retrying(ctx, c.buildRequest(messages, defs), true)
}

// buildRequest assembles the wire body; defs non-nil turns on native tools.
func (c *LLMClient) buildRequest(messages []core.Message, defs []*core.ToolDef) []byte {
	req := chatRequest{
		Model:       c.config.Model,
		Messages:    wireMessages(messages),
		Temperature: c.config.Temperature,
		MaxTokens:   c.config.MaxTokens,
	}
	if len(defs) > 0 {
		req.Tools = make([]openAITool, 0, len(defs))
		for _, def := range defs {
			req.Tools = append(req.Tools, openAITool{
				Type: "function",
				Function: toolFunction{
					Name:        def.Name,
					Description: def.Description,
					Parameters:  jsonSchemaFor(def),
				},
			})
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		// Marshalling tool definitions cannot fail for anything but
		// unencodable values; fall back to a body without tools rather than
		// panicking, and the request still goes through on the prompt shape.
		body, _ = json.Marshal(chatRequest{
			Model: c.config.Model, Messages: wireMessages(messages),
			Temperature: c.config.Temperature, MaxTokens: c.config.MaxTokens,
		})
	}
	return body
}

// wireMessages converts the conversation to the wire shape: native history
// carries tool_calls on the assistant and a tool_call_id on the answer.
func wireMessages(messages []core.Message) []chatMessage {
	out := make([]chatMessage, len(messages))
	for i, m := range messages {
		out[i] = chatMessage{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	}
	return out
}

// retrying runs the request with exponential backoff (1s, 2s, 4s...).
func (c *LLMClient) retrying(ctx context.Context, body []byte, native bool) (core.ChatResult, error) {
	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return core.ChatResult{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		result, err := c.chatOnce(ctx, body, native)
		if err == nil {
			return result, nil
		}
		if isRetryable(err) {
			lastErr = err
			continue
		}
		return core.ChatResult{}, err
	}
	return core.ChatResult{}, fmt.Errorf("LLM API failed after %d retries: %w", c.config.MaxRetries, lastErr)
}

// chatOnce performs one HTTP round trip and classifies the outcome.
func (c *LLMClient) chatOnce(ctx context.Context, body []byte, native bool) (core.ChatResult, error) {
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

	if resp.StatusCode != http.StatusOK {
		// The tools field specifically was refused: latch the capability
		// and surface the sentinel instead of a status dump.
		if native && toolsUnsupported(resp.StatusCode, respBody) {
			c.toolsBroken.Store(true)
			return core.ChatResult{}, fmt.Errorf("%w (status %d): %s", ErrToolsUnsupported, resp.StatusCode, truncateBody(respBody, 300))
		}

		apiErr := fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, truncateBody(respBody, 500))

		// Transient conditions are worth another attempt.
		if isRetryableStatus(resp.StatusCode) {
			return core.ChatResult{}, &retryableError{err: apiErr}
		}

		// 400/413 raised because the request exceeded the context window can
		// never succeed on retry, so say why instead of dumping a raw status.
		if isContextOverflow(resp.StatusCode, respBody) {
			return core.ChatResult{}, fmt.Errorf(
				"LLM context window exceeded (status %d); the request is too large — "+
					"reduce the prompt or lower the compaction threshold: %w",
				resp.StatusCode, apiErr)
		}

		return core.ChatResult{}, apiErr
	}

	return decodeCompletion(respBody, native)
}

// decodeCompletion parses the provider response, including native tool_calls.
func decodeCompletion(respBody []byte, native bool) (core.ChatResult, error) {
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

	choice := chatResp.Choices[0]
	result := core.ChatResult{
		Content:      choice.Message.Content,
		FinishReason: choice.FinishReason,
		Usage: core.TokenUsage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		},
	}

	if !native || len(choice.Message.ToolCalls) == 0 {
		return result, nil
	}
	for _, call := range choice.Message.ToolCalls {
		arguments := map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
				return core.ChatResult{}, fmt.Errorf(
					"tool call %q: arguments are not valid JSON: %w", call.Function.Name, err)
			}
		}
		result.ToolCalls = append(result.ToolCalls, core.NativeToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
	}
	return result, nil
}

// truncateBody keeps an error body readable instead of dumping megabytes.
// (The package already has truncate for strings; this one is for raw bodies.)
func truncateBody(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// --- Retry helpers (see retrying / chatOnce above) ---

// isRetryableStatus reports whether an HTTP status is worth retrying.
// 429 (rate limited), 408 (request timeout), 409 (conflict) and all 5xx are
// transient; 413 is not, because resending the same payload cannot shrink it.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusConflict:
		return true
	}
	return code >= 500
}

// isContextOverflow reports whether a 400/413 response is the provider
// complaining that the request exceeded its context window.
func isContextOverflow(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	text := strings.ToLower(string(body))
	for _, marker := range []string{
		"context length",
		"context_length",
		"context window",
		"maximum context",
		"too many tokens",
		"token limit",
		"prompt is too long",
		"request too large",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// --- Native function calling (N2) ---

// ErrToolsUnsupported reports that this endpoint does not accept the native
// tools parameter. The caller switches to the prompt path for the rest of the
// run: it is a provider capability, not a transient fault, so retrying the
// same request can never succeed.
var ErrToolsUnsupported = fmt.Errorf("endpoint does not support native tool calling")

// ToolAwareLLM is the optional capability a client can advertise: send the
// tool definitions and receive native tool_calls. Keeping it a separate
// interface (rather than widening LLMClient) means every existing mock and
// fake client stays valid and stays on the prompt path — the new path is opt
// in per client, and its absence is a fact, not an error.
type ToolAwareLLM interface {
	ChatWithTools(ctx context.Context, messages []core.Message, defs []*core.ToolDef) (core.ChatResult, error)
}

// openAITool is the wire shape of one function tool.
type openAITool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// toolsUnsupported reports whether an HTTP error means "this provider has no
// tools API" rather than something transient. The markers are the phrasings
// OpenAI-compatible stacks produce for an unknown/invalid "tools" field.
func toolsUnsupported(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound && status != http.StatusUnprocessableEntity {
		return false
	}
	text := strings.ToLower(string(body))
	for _, marker := range []string{
		"unknown parameter",
		"unrecognized request argument",
		"unexpected keyword",
		"does not support tools",
		"tools is not supported",
		"invalid 'tools'",
		"extra inputs are not permitted",
		"unknown field",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

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
