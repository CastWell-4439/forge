package scheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/mcp"
)

// FeishuMCPPoller implements polling against Feishu's MCP endpoint.
// It reuses the existing MCP Client with an HTTP transport.
type FeishuMCPPoller struct {
	client *mcp.Client
}

// FeishuMCPConfig configures the Feishu MCP poller.
type FeishuMCPConfig struct {
	// Endpoint is the MCP server URL (e.g. "https://project.feishu.cn/mcp_server/v1")
	Endpoint string
	// Token is the authentication token for the MCP endpoint.
	Token string
}

// NewFeishuMCPPoller creates a poller that connects to Feishu MCP via HTTP transport.
func NewFeishuMCPPoller(ctx context.Context, transport mcp.Transport) (*FeishuMCPPoller, error) {
	client, err := mcp.NewClient(ctx, transport)
	if err != nil {
		return nil, fmt.Errorf("feishu mcp: init client: %w", err)
	}
	return &FeishuMCPPoller{client: client}, nil
}

// Poll queries Feishu MCP using the given tool/query and returns events.
// The source parameter maps to an MCP tool name (e.g. "list_todo", "search_by_mql").
// The query parameter is passed as the tool's filter argument.
func (p *FeishuMCPPoller) Poll(ctx context.Context, source, query string) ([]Event, error) {
	// Determine which MCP tool to call based on source
	toolName := mapSourceToTool(source)

	args := map[string]any{
		"action":   "todo",
		"page_num": 1,
	}
	if query != "" {
		args["query"] = query
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}

	result, err := p.client.CallTool(ctx, toolName, argsJSON)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", toolName, err)
	}

	// Parse result into events
	return parseFeishuResult(result)
}

// mapSourceToTool maps a workflow source identifier to an MCP tool name.
func mapSourceToTool(source string) string {
	switch source {
	case "feishu_mcp", "feishu_todo":
		return "list_todo"
	case "feishu_mql":
		return "search_by_mql"
	default:
		return source
	}
}

// parseFeishuResult converts MCP tool output into trigger events.
func parseFeishuResult(result string) ([]Event, error) {
	// The MCP result is JSON containing work items
	var items []map[string]any
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		// Try wrapping: result might be an object with a list field
		var wrapper struct {
			List  []map[string]any `json:"list"`
			Items []map[string]any `json:"items"`
			Data  []map[string]any `json:"data"`
		}
		if err2 := json.Unmarshal([]byte(result), &wrapper); err2 != nil {
			return nil, fmt.Errorf("parse result: %w (original: %w)", err2, err)
		}
		if wrapper.List != nil {
			items = wrapper.List
		} else if wrapper.Items != nil {
			items = wrapper.Items
		} else if wrapper.Data != nil {
			items = wrapper.Data
		}
	}

	events := make([]Event, 0, len(items))
	for _, item := range items {
		id := extractEventID(item)
		if id == "" {
			continue
		}
		events = append(events, Event{
			ID:      id,
			Payload: item,
		})
	}
	return events, nil
}

// extractEventID extracts a unique identifier from a work item.
func extractEventID(item map[string]any) string {
	// Try common ID fields
	for _, key := range []string{"work_item_id", "id", "ID", "task_id"} {
		if v, ok := item[key]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// NewFeishuPollFunc creates a PollFunc that uses the FeishuMCPPoller.
func NewFeishuPollFunc(poller *FeishuMCPPoller) PollFunc {
	return func(ctx context.Context, source, query string) ([]Event, error) {
		return poller.Poll(ctx, source, query)
	}
}
