package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HandlerFunc is the function signature for agent tool handlers.
// It receives context and input parameters, and returns output or an error.
type HandlerFunc func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)

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

// Register adds a tool definition and its handler to the registry.
func (r *ToolRegistry) Register(def *ToolDef, handler HandlerFunc) error {
	if def == nil {
		return fmt.Errorf("tool definition cannot be nil")
	}
	if def.Name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for tool %q", def.Name)
	}
	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("tool %q already registered", def.Name)
	}
	r.tools[def.Name] = def
	r.handlers[def.Name] = handler
	return nil
}

// GetTool returns the definition for a named tool, or nil if not found.
func (r *ToolRegistry) GetTool(name string) *ToolDef {
	return r.tools[name]
}

// GetHandler returns the handler function for a named tool, or nil if not found.
func (r *ToolRegistry) GetHandler(name string) HandlerFunc {
	return r.handlers[name]
}

// HasHandler checks if a handler is registered for the given name.
func (r *ToolRegistry) HasHandler(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// ListTools returns all registered tool definitions sorted by name.
func (r *ToolRegistry) ListTools() []*ToolDef {
	result := make([]*ToolDef, 0, len(r.tools))
	for _, def := range r.tools {
		result = append(result, def)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// ListHandlerNames returns all registered handler names sorted.
func (r *ToolRegistry) ListHandlerNames() []string {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	return len(r.tools)
}

// List is an alias for ListTools.
func (r *ToolRegistry) List() []*ToolDef {
	return r.ListTools()
}

// FormatForPrompt generates a human/LLM-readable description of all available tools.
func (r *ToolRegistry) FormatForPrompt() string {
	tools := r.ListTools()
	var sb strings.Builder
	sb.WriteString("Available tools:\n\n")

	byCategory := make(map[string][]*ToolDef)
	for _, t := range tools {
		cat := t.Category
		if cat == "" {
			cat = "other"
		}
		byCategory[cat] = append(byCategory[cat], t)
	}

	categories := make([]string, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Strings(categories)

	for _, cat := range categories {
		sb.WriteString(fmt.Sprintf("## %s\n", cat))
		for _, t := range byCategory[cat] {
			sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", t.Name, t.DisplayName, t.Description))
			if len(t.InputSchema) > 0 {
				sb.WriteString("  Params: ")
				params := make([]string, 0, len(t.InputSchema))
				for name, def := range t.InputSchema {
					req := ""
					if def.Required {
						req = "*"
					}
					params = append(params, fmt.Sprintf("%s%s(%s)", name, req, def.Type))
				}
				sort.Strings(params)
				sb.WriteString(strings.Join(params, ", "))
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// FindSimilar returns the most similar tool name for a given (possibly misspelled) name.
// Uses Levenshtein edit distance for accurate typo correction (#9).
// Returns empty string if no reasonable match is found.
func (r *ToolRegistry) FindSimilar(name string) string {
	name = strings.ToLower(name)
	bestName := ""
	bestDist := -1

	for toolName := range r.tools {
		dist := levenshtein(name, strings.ToLower(toolName))
		if bestDist < 0 || dist < bestDist {
			bestDist = dist
			bestName = toolName
		}
	}

	// Threshold: allow up to ~30% of the longer string's length as edits,
	// with a minimum of 3 edits and maximum of 5.
	maxLen := len(name)
	if len(bestName) > maxLen {
		maxLen = len(bestName)
	}
	threshold := maxLen * 3 / 10
	if threshold < 3 {
		threshold = 3
	}
	if threshold > 5 {
		threshold = 5
	}

	if bestDist >= 0 && bestDist <= threshold {
		return bestName
	}
	return ""
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	m, n := len(a), len(b)
	if m == 0 {
		return n
	}
	if n == 0 {
		return m
	}

	// Use single-row DP to save memory.
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}

	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			curr[j] = ins
			if del < curr[j] {
				curr[j] = del
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}

	return prev[n]
}

// similarityScore is kept for backward compatibility but FindSimilar now uses levenshtein.
func similarityScore(a, b string) int {
	return -levenshtein(strings.ToLower(a), strings.ToLower(b))
}
