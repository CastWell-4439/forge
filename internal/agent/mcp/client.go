package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Client is an MCP protocol client that communicates with a single MCP server
// through a Transport. It negotiates the protocol revision (modern per-request
// metadata, or the legacy handshake), then lists and invokes tools.
type Client struct {
	transport Transport
	info      ServerInfo
	// version is the revision in force for this connection, and modern says
	// whether requests carry `_meta` (see protocol.go). Both are decided once,
	// during negotiation.
	version string
	modern  bool
	// inputHandler answers server-to-client requests expressed as an
	// InputRequiredResult (H4). Nil means the refusing default.
	inputHandler InputHandler
}

// ServerInfo contains metadata returned by the MCP server during initialization.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolAnnotations is what an MCP server claims about a tool's behaviour.
//
// These are HINTS, not guarantees: they describe intent as the server sees it,
// and a malicious or buggy server can claim readOnlyHint while deleting data.
// The bridge therefore maps them into the agent's effect classes without ever
// letting a "read-only" claim relax another constraint (see bridge.go).
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ToolDefinition is a tool discovered via MCP's tools/list method.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
	// Annotations carries the server's declared behaviour hints. Dropping them
	// (as this type used to) threw away the only information the server offers
	// about whether a tool reads or destroys — leaving every remote tool to be
	// judged as "undeclared", which is safe but blind.
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
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

// NewClient creates an MCP client over the given transport and negotiates the
// protocol revision with the server (see negotiateProtocol).
func NewClient(ctx context.Context, transport Transport) (*Client, error) {
	c := &Client{transport: transport}
	if err := c.negotiateProtocol(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ProtocolVersion returns the revision in force for this connection.
func (c *Client) ProtocolVersion() string { return c.version }

// IsModern reports whether this connection uses the per-request-metadata
// revision rather than the legacy handshake.
func (c *Client) IsModern() bool { return c.modern }

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
//
// A modern server may answer with an InputRequiredResult instead of a final
// result; that round trip is handled by callToolWithInput, so callers keep
// seeing "the tool's output or an error" rather than a protocol detail.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	result, err := c.callToolWithInput(ctx, name, arguments)
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w", name, err)
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

// call is a helper that creates a request, sends it, and checks for errors.
func (c *Client) call(ctx context.Context, method string, params interface{}) (Response, error) {
	return c.send(ctx, method, params, false)
}

// callMeta is call for the modern revision: the request carries the `_meta`
// envelope (version, capabilities, identity), as every request must once the
// handshake is gone.
func (c *Client) callMeta(ctx context.Context, method string, params interface{}) (Response, error) {
	return c.send(ctx, method, params, true)
}

func (c *Client) send(ctx context.Context, method string, params interface{}, withMeta bool) (Response, error) {
	// Modern requests carry their protocol version per request; a legacy
	// server ignores the field, so negotiation can probe with it either way.
	if withMeta || c.modern {
		var err error
		params, err = withMetaEnvelope(params)
		if err != nil {
			return Response{}, err
		}
	}

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

// withMetaEnvelope merges the reserved `_meta` keys into a request's params.
//
// The MCP specification puts `_meta` inside params; a request that has no
// params still needs the envelope, so an empty object is created for it. The
// caller's own params survive: only the reserved keys are written.
func withMetaEnvelope(params interface{}) (map[string]any, error) {
	envelope := map[string]any{}

	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for _meta envelope: %w", err)
		}
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return nil, fmt.Errorf("params are not an object, cannot carry _meta: %w", err)
		}
	}
	envelope["_meta"] = metaEnvelope()
	return envelope, nil
}

func (c *Client) nextID() int64 {
	if st, ok := c.transport.(*StdioTransport); ok {
		return st.NextID()
	}
	// Fallback for other transports (e.g. mock).
	return 1
}

// extractText flattens an MCP tool result into text for the model.
//
// It joins every text block rather than returning only the first one, and keeps a
// placeholder for non-text blocks (image/audio/resource). The previous version
// returned the first text block and silently dropped everything else, so a server
// that returned several blocks or only non-text content looked like an empty reply.
func extractText(content []toolContent) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		if c.Type == "text" {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s content omitted]", c.Type))
	}
	return strings.Join(parts, "\n")
}
