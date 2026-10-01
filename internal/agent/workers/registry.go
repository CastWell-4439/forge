package workers

import (
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
)

// ToolDef and its companions are aliases of the core definitions, which live in
// core/ for dependency direction compliance. The aliases keep existing code in
// workers/ compiling unchanged.
type ToolDef = core.ToolDef
type ParamDef = core.ParamDef
type ToolRegistry = core.ToolRegistry
type HandlerFunc = core.HandlerFunc

// NewToolRegistry creates a new empty tool registry.
var NewToolRegistry = core.NewToolRegistry

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
