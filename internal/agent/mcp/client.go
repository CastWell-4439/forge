package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Client is an MCP protocol client that communicates with a single MCP server
// through a Transport. It handles the MCP initialization handshake, tool
// listing, and tool invocation.
type Client struct {
	transport Transport
	info      ServerInfo
}

// ServerInfo contains metadata returned by the MCP server during initialization.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolDefinition is a tool discovered via MCP's tools/list method.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

// --- MCP Protocol Messages ---

type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      ServerInfo `json:"serverInfo"`
}

type toolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Client Methods ---

// NewClient creates an MCP client over the given transport, performs the
// MCP initialization handshake, and sends the "initialized" notification.
func NewClient(ctx context.Context, transport Transport) (*Client, error) {
	c := &Client{transport: transport}
	if err := c.initialize(ctx); err != nil {
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}
	return c, nil
}

// ServerName returns the name of the connected MCP server.
func (c *Client) ServerName() string { return c.info.Name }

// ListTools calls tools/list to discover available tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server and returns the text result.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	params := toolCallParams{
		Name:      name,
		Arguments: arguments,
	}

	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w", name, err)
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("parse tools/call result: %w", err)
	}

	if result.IsError {
		text := extractText(result.Content)
		return "", fmt.Errorf("tool %q returned error: %s", name, text)
	}

	return extractText(result.Content), nil
}

// Close shuts down the transport.
func (c *Client) Close() error {
	return c.transport.Close()
}

// --- Internal ---

func (c *Client) initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      clientInfo{Name: "forge-agent", Version: "1.0.0"},
	}

	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}

	var result initializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}
	c.info = result.ServerInfo

	// Send "initialized" notification per MCP spec.
	return c.transport.Notify(ctx, Notification{Method: "notifications/initialized"})
}

// call is a helper that creates a request, sends it, and checks for errors.
func (c *Client) call(ctx context.Context, method string, params interface{}) (Response, error) {
	req, err := NewRequest(c.nextID(), method, params)
	if err != nil {
		return Response{}, err
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return Response{}, err
	}

	if resp.Error != nil {
		return Response{}, resp.Error
	}

	return resp, nil
}

func (c *Client) nextID() int64 {
	if st, ok := c.transport.(*StdioTransport); ok {
		return st.NextID()
	}
	// Fallback for other transports (e.g. mock).
	return 1
}

func extractText(content []toolContent) string {
	for _, c := range content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}
