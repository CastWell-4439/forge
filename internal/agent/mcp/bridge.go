package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

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

		inputSchema, requiredParams := convertInputSchema(tool.InputSchema)

		def := &core.ToolDef{
			Name:           tool.Name,
			DisplayName:    tool.Name,
			Category:       "mcp",
			Description:    tool.Description,
			InputSchema:    inputSchema,
			RequiredParams: requiredParams,
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

// convertInputSchema turns the JSON Schema an MCP server advertises into the
// ParamDef map the tool registry expects, preserving types, descriptions and
// which parameters are required. Required names are sorted so the generated
// prompt is stable across runs.
func convertInputSchema(schema map[string]interface{}) (map[string]core.ParamDef, []string) {
	if len(schema) == 0 {
		return nil, nil
	}

	properties, _ := schema["properties"].(map[string]interface{})
	if len(properties) == 0 {
		return nil, nil
	}

	required := make(map[string]bool)
	if list, ok := schema["required"].([]interface{}); ok {
		for _, item := range list {
			if name, ok := item.(string); ok {
				required[name] = true
			}
		}
	}

	defs := make(map[string]core.ParamDef, len(properties))
	var requiredNames []string
	for name, raw := range properties {
		prop, _ := raw.(map[string]interface{})

		def := core.ParamDef{Required: required[name]}
		if t, ok := prop["type"].(string); ok {
			def.Type = t
		}
		if d, ok := prop["description"].(string); ok {
			def.Description = d
		}
		defs[name] = def

		if required[name] {
			requiredNames = append(requiredNames, name)
		}
	}
	sort.Strings(requiredNames)

	return defs, requiredNames
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
