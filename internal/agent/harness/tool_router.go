package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// ToolRouter bridges the Agent Harness with the ToolRegistry from Phase A1/A2.
// It looks up tools by name and invokes their handlers, returning results
// in a format the ReAct loop can feed back to the LLM.
type ToolRouter struct {
	registry *workers.ToolRegistry
}

// NewToolRouter creates a ToolRouter wrapping the given worker ToolRegistry.
func NewToolRouter(registry *workers.ToolRegistry) *ToolRouter {
	return &ToolRouter{registry: registry}
}

// Call invokes a tool by name with the given parameters.
// Returns a ToolResult with the output or error message.
func (r *ToolRouter) Call(ctx context.Context, name string, params map[string]interface{}) *core.ToolResult {
	// Check if the tool exists.
	toolDef := r.registry.GetTool(name)
	if toolDef == nil {
		similar := r.findSimilar(name)
		msg := fmt.Sprintf("unknown tool %q", name)
		if similar != "" {
			msg += fmt.Sprintf(", did you mean %q?", similar)
		}
		return &core.ToolResult{Error: msg}
	}

	// Get the handler function.
	handler := r.registry.GetHandler(name)
	if handler == nil {
		return &core.ToolResult{Error: fmt.Sprintf("tool %q has no handler registered", name)}
	}

	// Execute the handler (HandlerFunc signature: func(ctx, params map) (map, error)).
	result, err := handler(ctx, params)
	if err != nil {
		return &core.ToolResult{Error: fmt.Sprintf("tool %q failed: %s", name, err.Error())}
	}

	// Serialize result to string for the LLM.
	output, err := json.Marshal(result)
	if err != nil {
		return &core.ToolResult{Output: fmt.Sprintf("%v", result)}
	}

	return &core.ToolResult{Output: string(output)}
}

// ListTools returns descriptions of all available tools formatted for an LLM prompt.
func (r *ToolRouter) ListTools() string {
	tools := r.registry.ListTools()
	if len(tools) == 0 {
		return "No tools available."
	}

	result := "Available tools:\n"
	for _, t := range tools {
		result += fmt.Sprintf("- %s: %s\n", t.Name, t.Description)
		if len(t.RequiredParams) > 0 {
			result += fmt.Sprintf("  Required params: %v\n", t.RequiredParams)
		}
	}
	return result
}

// findSimilar looks for a tool with a similar name (basic prefix match).
func (r *ToolRouter) findSimilar(name string) string {
	tools := r.registry.ListTools()
	bestScore := 0
	bestName := ""

	for _, t := range tools {
		score := commonPrefixLen(name, t.Name)
		if score > bestScore && score >= 3 {
			bestScore = score
			bestName = t.Name
		}
	}
	return bestName
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
