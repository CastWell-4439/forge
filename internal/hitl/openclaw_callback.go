package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenClawConfig configures the OpenClaw integration.
type OpenClawConfig struct {
	// BaseURL is the OpenClaw gateway endpoint (e.g. "http://localhost:3000").
	BaseURL string `yaml:"base_url"`
	// SessionKey is the OpenClaw session to send messages to.
	SessionKey string `yaml:"session_key"`
	// Channel is the target channel (e.g. "feishu").
	Channel string `yaml:"channel"`
	// Timeout for HTTP requests to OpenClaw.
	Timeout time.Duration `yaml:"timeout"`
}

// OpenClawCallback implements HITLCallback by sending messages via OpenClaw.
type OpenClawCallback struct {
	config     OpenClawConfig
	client     HTTPClient
	formatter  *MessageFormatter
}

// HTTPClient interface for testability.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewOpenClawCallback creates a callback that sends HITL notifications through OpenClaw.
func NewOpenClawCallback(cfg OpenClawConfig, client HTTPClient) *OpenClawCallback {
	if client == nil {
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	return &OpenClawCallback{
		config:    cfg,
		client:    client,
		formatter: NewMessageFormatter(),
	}
}

// openclawMessage is the payload sent to OpenClaw's message endpoint.
type openclawMessage struct {
	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Message    string `json:"message"`
}

// Notify sends a HITL request notification through OpenClaw.
// Implements HITLCallback signature.
func (c *OpenClawCallback) Notify(ctx context.Context, req *Request) error {
	msg := c.formatter.FormatRequest(req)

	payload := openclawMessage{
		SessionKey: c.config.SessionKey,
		Channel:    c.config.Channel,
		Message:    msg,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("openclaw callback: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/api/sessions/send", c.config.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openclaw callback: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openclaw callback: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("openclaw callback: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
