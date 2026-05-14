package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// ServerConfig describes how to connect to one MCP server.
type ServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// Manager manages multiple MCP server lifecycles.
// It implements core.MCPManager.
type Manager struct {
	configs []ServerConfig

	mu      sync.RWMutex
	clients map[string]*Client // name -> client
	tools   map[string]string  // tool name -> server name (for routing)
}

// NewManager creates a Manager with the given server configurations.
func NewManager(configs []ServerConfig) *Manager {
	return &Manager{
		configs: configs,
		clients: make(map[string]*Client),
		tools:   make(map[string]string),
	}
}

// Start connects to all configured MCP servers and discovers their tools.
func (m *Manager) Start(ctx context.Context) error {
	for _, cfg := range m.configs {
		if err := m.startServer(ctx, cfg); err != nil {
			// Log but continue — partial availability is acceptable.
			log.Printf("[mcp] failed to start server %q: %v", cfg.Name, err)
			continue
		}
	}
	return nil
}

// Stop shuts down all connected MCP servers.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	for name, client := range m.clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close server %q: %w", name, err)
		}
	}
	m.clients = make(map[string]*Client)
	m.tools = make(map[string]string)
	return firstErr
}

// ListTools returns all tools discovered across all connected servers.
func (m *Manager) ListTools(_ context.Context) ([]core.MCPToolDef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []core.MCPToolDef
	for toolName := range m.tools {
		// Build a minimal definition. Full schema is used at call time.
		result = append(result, core.MCPToolDef{
			Name:        toolName,
			Description: "", // Could cache descriptions from discovery
		})
	}
	return result, nil
}

// CallTool routes a tool call to the correct MCP server.
func (m *Manager) CallTool(ctx context.Context, name string, params json.RawMessage) (*core.ToolResult, error) {
	m.mu.RLock()
	serverName, ok := m.tools[name]
	if !ok {
		m.mu.RUnlock()
		return &core.ToolResult{Error: fmt.Sprintf("MCP tool %q not found", name)}, nil
	}
	client, ok := m.clients[serverName]
	m.mu.RUnlock()

	if !ok {
		return &core.ToolResult{Error: fmt.Sprintf("MCP server %q not connected", serverName)}, nil
	}

	output, err := client.CallTool(ctx, name, params)
	if err != nil {
		return &core.ToolResult{Error: err.Error()}, nil
	}

	return &core.ToolResult{Output: output}, nil
}

// --- Internal ---

func (m *Manager) startServer(ctx context.Context, cfg ServerConfig) error {
	transport, err := NewStdioTransport(StdioConfig{
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     cfg.Env,
	})
	if err != nil {
		return err
	}

	client, err := NewClient(ctx, transport)
	if err != nil {
		_ = transport.Close()
		return err
	}

	// Discover tools.
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("list tools: %w", err)
	}

	m.mu.Lock()
	m.clients[cfg.Name] = client
	for _, tool := range tools {
		m.tools[tool.Name] = cfg.Name
	}
	m.mu.Unlock()

	log.Printf("[mcp] connected to %q (%s), discovered %d tools",
		cfg.Name, client.ServerName(), len(tools))

	return nil
}

// Ensure Manager implements core.MCPManager.
var _ core.MCPManager = (*Manager)(nil)
