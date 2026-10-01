package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// DefaultToolTimeout bounds a single tool invocation when neither the router nor
// the tool definition sets a deadline. Tool dispatch previously had no deadline
// at all, so one hung handler hung the entire run.
const DefaultToolTimeout = 5 * time.Minute

// ToolRouter bridges the Agent Harness with the ToolRegistry from Phase A1/A2.
// It looks up tools by name and invokes their handlers, returning results
// in a format the ReAct loop can feed back to the LLM.
type ToolRouter struct {
	registry    *core.ToolRegistry
	toolTimeout time.Duration
}

// NewToolRouter creates a ToolRouter wrapping the given worker ToolRegistry.
func NewToolRouter(registry *core.ToolRegistry) *ToolRouter {
	return &ToolRouter{registry: registry, toolTimeout: DefaultToolTimeout}
}

// WithToolTimeout overrides the default per-invocation deadline. Pass 0 to
// disable the cap entirely.
func (r *ToolRouter) WithToolTimeout(d time.Duration) *ToolRouter {
	r.toolTimeout = d
	return r
}

// Call invokes a tool by name with the given parameters.
// Returns a ToolResult with the output or error message.
func (r *ToolRouter) Call(ctx context.Context, name string, params map[string]interface{}) *core.ToolResult {
	// Check if the tool exists.
	toolDef := r.registry.GetTool(name)
	if toolDef == nil {
		// ToolRegistry.FindSimilar uses length-adaptive Levenshtein distance;
		// the previous local prefix matcher missed most real typos.
		similar := r.registry.FindSimilar(name)
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

	// Adapt parameter types: JSON unmarshaling turns all numbers into float64,
	// but handlers expect int/int64 for "integer" schema fields.
	adaptParams(params, toolDef.InputSchema)

	// Execute the handler under a deadline and a panic guard. A misbehaving tool
	// must not be able to hang the whole run or kill the process.
	timeout := r.toolTimeout
	if toolDef.Timeout > 0 {
		timeout = toolDef.Timeout
	}
	callCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	result, err := callHandlerSafely(callCtx, name, handler, params)
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

// callHandlerSafely invokes a tool handler and converts a panic into an error, so
// that one broken tool cannot take the process down with it.
func callHandlerSafely(ctx context.Context, name string, handler core.HandlerFunc, params map[string]interface{}) (result map[string]interface{}, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result = nil
			err = fmt.Errorf("panicked: %v", rec)
		}
	}()
	return handler(ctx, params)
}

// adaptParams converts JSON-deserialized float64 values to int64 where the
// tool's InputSchema declares an "integer" type. Without this, handler code
// doing params["x"].(int) would panic because json.Unmarshal always produces float64.
func adaptParams(params map[string]interface{}, schema map[string]core.ParamDef) {
	if params == nil || schema == nil {
		return
	}
	for key, val := range params {
		def, ok := schema[key]
		if !ok {
			continue
		}
		switch def.Type {
		case "integer":
			if f, ok := val.(float64); ok {
				params[key] = int64(f)
			}
		}
	}
}
