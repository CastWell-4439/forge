// Package mcp implements the MCP Worker for Forge workflows.
// It connects to Feishu Project MCP via HTTPTransport and exposes
// workflow-level actions (get_workitem, search, update, etc.).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	agentmcp "github.com/castwell/forge/internal/agent/mcp"
)

// Worker is the MCP workflow worker.
type Worker struct {
	client *agentmcp.Client
}

// WorkerConfig configures the MCP Worker.
type WorkerConfig struct {
	Endpoint string // MCP server URL
	Token    string // Bearer token
}

// NewWorker creates an MCP Worker connected to the given endpoint.
func NewWorker(ctx context.Context, cfg WorkerConfig) (*Worker, error) {
	transport := agentmcp.NewHTTPTransport(agentmcp.HTTPTransportConfig{
		Endpoint: cfg.Endpoint,
		Token:    cfg.Token,
	})
	client, err := agentmcp.NewClient(ctx, transport)
	if err != nil {
		return nil, fmt.Errorf("mcp worker: init: %w", err)
	}
	return &Worker{client: client}, nil
}

// NewWorkerFromClient creates an MCP Worker from an existing client (for testing).
func NewWorkerFromClient(client *agentmcp.Client) *Worker {
	return &Worker{client: client}
}

// Execute runs an action with the given parameters.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	switch action {
	case "get_workitem":
		return w.getWorkitem(ctx, params)
	case "search_workitems":
		return w.searchWorkitems(ctx, params)
	case "list_todo":
		return w.listTodo(ctx, params)
	case "update_field":
		return w.updateField(ctx, params)
	case "transition_state":
		return w.transitionState(ctx, params)
	case "add_comment":
		return w.addComment(ctx, params)
	case "list_comments":
		return w.listComments(ctx, params)
	default:
		return "", fmt.Errorf("mcp worker: unknown action %q", action)
	}
}

// callTool is a helper that marshals params and calls the MCP tool.
func (w *Worker) callTool(ctx context.Context, tool string, params map[string]any) (string, error) {
	args, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("mcp worker: marshal params: %w", err)
	}
	return w.client.CallTool(ctx, tool, args)
}
