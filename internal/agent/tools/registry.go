// Package tools provides a higher-level tool registry for the Agent layer.
// It wraps the lower-level workers.ToolRegistry and adds LLM-facing features
// such as FormatForPrompt and a DefaultRegistry pre-loaded with all 18 tools.
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/castwell/forge/internal/agent/workers"
)

// ToolRegistry is the Agent-layer tool registry. It wraps the Phase A1
// workers.ToolRegistry and adds LLM-facing features (FormatForPrompt,
// DefaultRegistry, FindSimilar).
type ToolRegistry struct {
	inner *workers.ToolRegistry
}

// NewToolRegistry creates a new higher-level tool registry wrapping the given
// workers.ToolRegistry.
func NewToolRegistry(inner *workers.ToolRegistry) *ToolRegistry {
	return &ToolRegistry{inner: inner}
}

// Register delegates to the inner registry.
func (r *ToolRegistry) Register(def *workers.ToolDef, handler workers.HandlerFunc) error {
	return r.inner.Register(def, handler)
}

// Get returns the tool definition for the given handler name.
func (r *ToolRegistry) Get(name string) *workers.ToolDef {
	return r.inner.GetTool(name)
}

// List returns all registered tool definitions sorted by name.
func (r *ToolRegistry) List() []*workers.ToolDef {
	tools := r.inner.ListTools()
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}

// HasHandler returns true if a handler with the given name is registered.
func (r *ToolRegistry) HasHandler(name string) bool {
	return r.inner.HasHandler(name)
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
			// Sort parameter names for deterministic output.
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
// unknown handler name using simple Levenshtein-like prefix/contains matching.
func (r *ToolRegistry) FindSimilar(name string) string {
	tools := r.inner.ListTools()
	if len(tools) == 0 {
		return ""
	}

	nameLower := strings.ToLower(name)
	best := ""
	bestScore := -1

	for _, t := range tools {
		tLower := strings.ToLower(t.Name)
		score := 0

		// Exact prefix match scores highest.
		if strings.HasPrefix(tLower, nameLower) || strings.HasPrefix(nameLower, tLower) {
			score = 100
		}
		// Same category prefix (e.g. "ai." matches "ai.face_swap").
		parts := strings.SplitN(nameLower, ".", 2)
		tParts := strings.SplitN(tLower, ".", 2)
		if len(parts) > 0 && len(tParts) > 0 && parts[0] == tParts[0] {
			score += 50
		}
		// Contains match.
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

// DefaultRegistry creates a ToolRegistry pre-loaded with all 18 tools from
// the Phase A1 worker handlers in mock mode.
// Returns an error if registration fails (should not happen with valid defs).
func DefaultRegistry() (*ToolRegistry, error) {
	inner := workers.NewToolRegistry()
	cfg := workers.HandlerConfig{
		Mode:      workers.HandlerModeMock,
		Workspace: "/tmp/forge",
	}
	if err := workers.RegisterAll(inner, cfg); err != nil {
		return nil, fmt.Errorf("default registry: %w", err)
	}
	return NewToolRegistry(inner), nil
}
