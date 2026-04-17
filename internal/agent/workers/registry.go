package workers

import (
	"fmt"
	"time"
)

// ToolDef describes a single tool/handler available to the Agent for DAG planning.
// The Agent uses these definitions to select tools and validate parameters.
type ToolDef struct {
	// Identity
	Name        string `yaml:"name"`         // Forge handler name, e.g. "ai.face_swap"
	DisplayName string `yaml:"display_name"` // Human-readable name, e.g. "AI Face Swap"
	Category    string `yaml:"category"`     // "video" | "audio" | "ai" | "media" | "quality"

	// Capability description (consumed by LLM for tool selection)
	Description string `yaml:"description"`

	// Parameter definitions
	InputSchema  map[string]ParamDef `yaml:"input_schema"`
	OutputSchema map[string]ParamDef `yaml:"output_schema"`

	// Required input parameter names
	RequiredParams []string `yaml:"required_params"`

	// Constraints
	RequiresGPU   bool          `yaml:"requires_gpu"`
	EstimatedTime time.Duration `yaml:"estimated_time"`
	MaxInputSize  int64         `yaml:"max_input_size"`

	// Dependency hints for DAG generation
	TypicalPredecessors []string `yaml:"typical_predecessors"`
	TypicalSuccessors   []string `yaml:"typical_successors"`
}

// ParamDef describes a single parameter in a tool's input or output schema.
type ParamDef struct {
	Type        string `yaml:"type"`        // "string" | "integer" | "number" | "boolean" | "array" | "object"
	Description string `yaml:"description"` // Human-readable description
	Required    bool   `yaml:"required"`
}

// ToolRegistry maintains a mapping of handler names to tool definitions and handler functions.
type ToolRegistry struct {
	tools    map[string]*ToolDef
	handlers map[string]HandlerFunc
}

// NewToolRegistry creates a new empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:    make(map[string]*ToolDef),
		handlers: make(map[string]HandlerFunc),
	}
}

// Register adds a tool definition and its handler function to the registry.
func (r *ToolRegistry) Register(def *ToolDef, handler HandlerFunc) error {
	if def == nil {
		return fmt.Errorf("register tool: definition is nil")
	}
	if def.Name == "" {
		return fmt.Errorf("register tool: name is empty")
	}
	if handler == nil {
		return fmt.Errorf("register tool %s: handler is nil", def.Name)
	}
	r.tools[def.Name] = def
	r.handlers[def.Name] = handler
	return nil
}

// GetTool returns the tool definition for the given handler name, or nil if not found.
func (r *ToolRegistry) GetTool(name string) *ToolDef {
	return r.tools[name]
}

// GetHandler returns the handler function for the given name, or nil if not found.
func (r *ToolRegistry) GetHandler(name string) HandlerFunc {
	return r.handlers[name]
}

// HasHandler returns true if a handler with the given name is registered.
func (r *ToolRegistry) HasHandler(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// ListTools returns all registered tool definitions.
func (r *ToolRegistry) ListTools() []*ToolDef {
	result := make([]*ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

// ListHandlerNames returns all registered handler names.
func (r *ToolRegistry) ListHandlerNames() []string {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	return names
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	return len(r.tools)
}
