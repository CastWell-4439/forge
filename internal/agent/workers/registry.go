package workers

import (
	"fmt"
	"sort"
	"strings"
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

// List returns all registered tool definitions sorted by name.
func (r *ToolRegistry) List() []*ToolDef {
	result := make([]*ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// FormatForPrompt formats all registered tools as text suitable for inclusion
// in an LLM prompt. Each tool is described with its handler name, display name,
// category, description, input parameters, and constraints.
func (r *ToolRegistry) FormatForPrompt() string {
	tools := r.List()
	var sb strings.Builder

	for i, tool := range tools {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("### %s (%s)\n", tool.Name, tool.DisplayName))
		sb.WriteString(fmt.Sprintf("Category: %s\n", tool.Category))
		sb.WriteString(fmt.Sprintf("Description: %s\n", tool.Description))

		if tool.RequiresGPU {
			sb.WriteString("Requires GPU: yes\n")
		}
		if tool.EstimatedTime > 0 {
			sb.WriteString(fmt.Sprintf("Estimated time: %s\n", tool.EstimatedTime))
		}

		if len(tool.InputSchema) > 0 {
			sb.WriteString("Input parameters:\n")
			paramNames := make([]string, 0, len(tool.InputSchema))
			for pn := range tool.InputSchema {
				paramNames = append(paramNames, pn)
			}
			sort.Strings(paramNames)

			requiredSet := make(map[string]bool, len(tool.RequiredParams))
			for _, rp := range tool.RequiredParams {
				requiredSet[rp] = true
			}

			for _, pn := range paramNames {
				p := tool.InputSchema[pn]
				reqLabel := ""
				if requiredSet[pn] || p.Required {
					reqLabel = " (required)"
				}
				sb.WriteString(fmt.Sprintf("  - %s: %s — %s%s\n", pn, p.Type, p.Description, reqLabel))
			}
		}

		if len(tool.TypicalPredecessors) > 0 {
			sb.WriteString(fmt.Sprintf("Typical predecessors: %s\n", strings.Join(tool.TypicalPredecessors, ", ")))
		}
		if len(tool.TypicalSuccessors) > 0 {
			sb.WriteString(fmt.Sprintf("Typical successors: %s\n", strings.Join(tool.TypicalSuccessors, ", ")))
		}
	}

	return sb.String()
}

// FindSimilar returns the most similar registered handler name for the given
// unknown handler name using prefix/contains matching.
func (r *ToolRegistry) FindSimilar(name string) string {
	tools := r.ListTools()
	if len(tools) == 0 {
		return ""
	}

	nameLower := strings.ToLower(name)
	best := ""
	bestScore := -1

	for _, t := range tools {
		tLower := strings.ToLower(t.Name)
		score := 0

		if strings.HasPrefix(tLower, nameLower) || strings.HasPrefix(nameLower, tLower) {
			score = 100
		}
		parts := strings.SplitN(nameLower, ".", 2)
		tParts := strings.SplitN(tLower, ".", 2)
		if len(parts) > 0 && len(tParts) > 0 && parts[0] == tParts[0] {
			score += 50
		}
		if strings.Contains(tLower, nameLower) || strings.Contains(nameLower, tLower) {
			score += 25
		}

		if score > bestScore {
			bestScore = score
			best = t.Name
		}
	}

	return best
}

// DefaultRegistry creates a ToolRegistry pre-loaded with all tools in mock mode.
func DefaultRegistry() (*ToolRegistry, error) {
	reg := NewToolRegistry()
	cfg := HandlerConfig{
		Mode:      HandlerModeMock,
		Workspace: "/tmp/forge",
	}
	if err := RegisterAll(reg, cfg); err != nil {
		return nil, fmt.Errorf("default registry: %w", err)
	}
	return reg, nil
}
