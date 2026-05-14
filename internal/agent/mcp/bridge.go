package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
)

// Bridge connects an MCPManager to a ToolRegistry.
// It discovers tools from all MCP servers and registers them as native
// Forge tools so the ReAct loop can call them transparently.
type Bridge struct {
	manager  *Manager
	registry *core.ToolRegistry
}

// NewBridge creates a bridge between an MCPManager and a ToolRegistry.
func NewBridge(manager *Manager, registry *core.ToolRegistry) *Bridge {
	return &Bridge{
		manager:  manager,
		registry: registry,
	}
}

// Sync discovers tools from all MCP servers and registers them in the ToolRegistry.
// Existing tools with the same name are skipped (native tools take precedence).
func (b *Bridge) Sync(ctx context.Context) (int, error) {
	tools, err := b.manager.ListTools(ctx)
	if err != nil {
		return 0, fmt.Errorf("list MCP tools: %w", err)
	}

	registered := 0
	for _, tool := range tools {
		// Skip if a native tool already has this name.
		if b.registry.HasHandler(tool.Name) {
			continue
		}

		def := &core.ToolDef{
			Name:        tool.Name,
			DisplayName: tool.Name,
			Category:    "mcp",
			Description: tool.Description,
		}

		// Create a handler that delegates to the MCP manager.
		handler := b.makeHandler(tool.Name)

		if err := b.registry.Register(def, handler); err != nil {
			// Log but continue — partial registration is acceptable.
			continue
		}
		registered++
	}

	return registered, nil
}

// makeHandler creates a HandlerFunc that calls the named MCP tool via the Manager.
func (b *Bridge) makeHandler(toolName string) core.HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		// Serialize params to JSON for MCP.
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for MCP tool %q: %w", toolName, err)
		}

		result, err := b.manager.CallTool(ctx, toolName, raw)
		if err != nil {
			return nil, err
		}

		if result.Error != "" {
			return nil, fmt.Errorf("%s", result.Error)
		}

		// Try to parse output as JSON map; if it fails, wrap as string.
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
			return map[string]interface{}{"output": result.Output}, nil
		}
		return out, nil
	}
}
