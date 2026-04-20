// Package agent is the top-level entry point for the Agent layer.
// It assembles all sub-packages (planning, session, tools, workers)
// and optional enhancement modules via functional options.
package agent

import "github.com/castwell/forge/internal/agent/core"

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
