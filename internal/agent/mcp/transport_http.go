package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// HTTPTransport communicates with an MCP server over HTTP POST (streamable HTTP).
// Each request is a single HTTP POST; the response body contains the JSON-RPC response.
type HTTPTransport struct {
	endpoint string
	token    string
	client   *http.Client
	nextID   atomic.Int64
}

// HTTPTransportConfig configures the HTTP transport.
type HTTPTransportConfig struct {
	Endpoint string // MCP server URL
	Token    string // Bearer token for auth
	Client   *http.Client
}

// NewHTTPTransport creates an HTTP-based MCP transport.
func NewHTTPTransport(cfg HTTPTransportConfig) *HTTPTransport {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPTransport{
		endpoint: cfg.Endpoint,
		token:    cfg.Token,
		client:   client,
	}
}

// Send sends a JSON-RPC request over HTTP POST and returns the response.
func (t *HTTPTransport) Send(ctx context.Context, req Request) (Response, error) {
	// Override ID with our own sequence
	req.ID = t.nextID.Add(1)

	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("http transport: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("http transport: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.token)
	}

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("http transport: do request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return Response{}, fmt.Errorf("http transport: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("http transport: read response: %w", err)
	}

	resp, err := ParseResponse(respBody)
	if err != nil {
		return Response{}, fmt.Errorf("http transport: parse response: %w", err)
	}

	return resp, nil
}

// Notify sends a JSON-RPC notification (fire-and-forget).
func (t *HTTPTransport) Notify(ctx context.Context, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("http transport: marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.token)
	}

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return err
	}
	httpResp.Body.Close()
	return nil
}

// Close is a no-op for HTTP transport.
func (t *HTTPTransport) Close() error { return nil }
