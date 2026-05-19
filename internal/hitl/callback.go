package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CallbackConfig configures the external HITL notification endpoint.
type CallbackConfig struct {
	// Endpoint is the URL to POST HITL requests to (e.g. OpenClaw webhook).
	Endpoint string
	// Token is the auth token for the callback endpoint.
	Token string
	// Client is the HTTP client to use (defaults to http.DefaultClient).
	Client *http.Client
}

// HITLNotification is the payload sent to the callback endpoint.
type HITLNotification struct {
	RequestID  string   `json:"request_id"`
	WorkflowID string   `json:"workflow_id"`
	TaskID     string   `json:"task_id"`
	Message    string   `json:"message"`
	Options    []string `json:"options"`
	TimeoutAt  string   `json:"timeout_at"` // RFC3339
}

// NewHTTPCallback creates an HITLCallback that POSTs notifications to an HTTP endpoint.
func NewHTTPCallback(cfg CallbackConfig) HITLCallback {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}

	return func(ctx context.Context, req *Request) error {
		notification := HITLNotification{
			RequestID:  req.ID,
			WorkflowID: req.WorkflowID,
			TaskID:     req.TaskID,
			Message:    req.Message,
			Options:    req.Options,
			TimeoutAt:  req.TimeoutAt.Format("2006-01-02T15:04:05Z07:00"),
		}

		body, err := json.Marshal(notification)
		if err != nil {
			return fmt.Errorf("hitl callback: marshal: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("hitl callback: create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if cfg.Token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			return fmt.Errorf("hitl callback: send: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			return fmt.Errorf("hitl callback: status %d", resp.StatusCode)
		}
		return nil
	}
}

// NewNoopCallback returns a callback that does nothing (for testing).
func NewNoopCallback() HITLCallback {
	return func(ctx context.Context, req *Request) error {
		return nil
	}
}
