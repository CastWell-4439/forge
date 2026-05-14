// Package agent is the top-level entry point for the Agent layer.
// It assembles all sub-packages (planning, session, tools, workers)
// and optional enhancement modules via functional options.
package agent

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/agent/mcp"
	"github.com/castwell/forge/internal/agent/workers"
)

// Agent is the top-level entry point that assembles all modules.
// Required dependencies are set in New(); optional modules are injected
// via WithXxx options — pass nil or omit to disable a module.
type Agent struct {
	// Required
	LLM    core.LLMClient
	Config core.AgentConfig

	// Optional enhancement modules (nil = disabled)
	InputGuard  core.InputGuard
	OutputGuard core.OutputGuard
	Budget      core.BudgetChecker
	Retriever   core.Retriever
	Memory      core.MemoryStore
	Checkpoint  core.CheckpointStore
	MCP         core.MCPManager
	Verifier    core.Verifier
}

// Option configures an optional module on the Agent.
type Option func(*Agent)

// WithInputGuard enables M6 input safety checks.
func WithInputGuard(g core.InputGuard) Option { return func(a *Agent) { a.InputGuard = g } }

// WithOutputGuard enables M6 output content filtering.
func WithOutputGuard(g core.OutputGuard) Option { return func(a *Agent) { a.OutputGuard = g } }

// WithBudget enables M6 token budget enforcement.
func WithBudget(b core.BudgetChecker) Option { return func(a *Agent) { a.Budget = b } }

// WithRetriever enables M3 RAG knowledge retrieval.
func WithRetriever(r core.Retriever) Option { return func(a *Agent) { a.Retriever = r } }

// WithMemory enables M5 short-term and long-term memory.
func WithMemory(m core.MemoryStore) Option { return func(a *Agent) { a.Memory = m } }

// WithCheckpoint enables M12 state persistence for crash recovery.
func WithCheckpoint(c core.CheckpointStore) Option { return func(a *Agent) { a.Checkpoint = c } }

// WithMCP enables M1 MCP tool discovery and invocation.
func WithMCP(m core.MCPManager) Option { return func(a *Agent) { a.MCP = m } }

// WithVerifier enables D5 self-verification loop.
func WithVerifier(v core.Verifier) Option { return func(a *Agent) { a.Verifier = v } }

// New creates an Agent with required dependencies and optional modules.
func New(llm core.LLMClient, opts ...Option) *Agent {
	a := &Agent{
		LLM:    llm,
		Config: core.DefaultConfig(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Run is the top-level entry point for executing an agent task.
// It assembles the ToolRouter, AgentLoop, injects all optional modules,
// starts MCP servers (if configured), and delegates to the ReAct loop.
//
// This is the single function external callers (schedulers, API handlers) invoke.
func (a *Agent) Run(ctx context.Context, sessionID string, userInput string) (*harness.RunResult, error) {
	// 1. Build ToolRegistry with all built-in handlers.
	registry := workers.NewToolRegistry()
	cfg := workers.HandlerConfig{
		Mode:      workers.HandlerModeMock, // TODO: make configurable
		Workspace: "/tmp/forge-workspace",
	}
	if err := workers.RegisterAll(registry, cfg); err != nil {
		return nil, fmt.Errorf("register tools: %w", err)
	}

	// 2. Start MCP servers and bridge their tools into the registry.
	if a.MCP != nil {
		if err := a.MCP.Start(ctx); err != nil {
			return nil, fmt.Errorf("start MCP: %w", err)
		}
		defer a.MCP.Stop()

		if mgr, ok := a.MCP.(*mcp.Manager); ok {
			bridge := mcp.NewBridge(mgr, registry)
			if _, err := bridge.Sync(ctx); err != nil {
				return nil, fmt.Errorf("sync MCP tools: %w", err)
			}
		}
	}

	// 3. Build the AgentLoop.
	router := harness.NewToolRouter(registry)
	loopCfg := harness.LoopConfig{
		MaxSteps:         a.Config.MaxSteps,
		MaxContextTokens: 128000,
	}
	loop := harness.NewAgentLoop(a.LLM, router, loopCfg)

	// 4. Inject optional modules.
	if a.InputGuard != nil {
		loop.SetInputGuard(a.InputGuard)
	}
	if a.OutputGuard != nil {
		loop.SetOutputGuard(a.OutputGuard)
	}
	if a.Budget != nil {
		loop.SetBudget(a.Budget)
	}
	if a.Checkpoint != nil {
		loop.SetCheckpoint(a.Checkpoint)
	}
	if a.Memory != nil {
		loop.SetMemory(a.Memory)
	}
	if a.Verifier != nil {
		loop.SetVerifier(a.Verifier)
	}

	// 5. Execute the ReAct loop.
	return loop.Run(ctx, sessionID, userInput)
}
