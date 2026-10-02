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

	// CheckpointFailurePolicy decides whether a failed checkpoint write is fatal.
	// Empty keeps the best-effort default.
	CheckpointFailurePolicy harness.CheckpointFailurePolicy

	// EffectFailurePolicy decides whether a failed budget/guard/verifier/memory
	// write is fatal. Empty keeps the best-effort default.
	EffectFailurePolicy harness.EffectFailurePolicy

	// MemoryWriteJudge overrides the default gate that decides whether a
	// finished run is worth remembering.
	MemoryWriteJudge harness.MemoryWriteJudge
}

// Option configures an optional module on the Agent.
type Option func(*Agent)

// WithInputGuard enables M6 input safety checks.
func WithInputGuard(g core.InputGuard) Option { return func(a *Agent) { a.InputGuard = g } }

// WithOutputGuard enables M6 output content filtering.
func WithOutputGuard(g core.OutputGuard) Option { return func(a *Agent) { a.OutputGuard = g } }

// WithBudget enables M6 token budget enforcement.
func WithBudget(b core.BudgetChecker) Option { return func(a *Agent) { a.Budget = b } }

// WithRetriever enables M3 RAG knowledge retrieval. The retriever backs the
// knowledge.search tool; retrieval itself stays agentic - the model decides
// when to search rather than having results pushed into every prompt.
func WithRetriever(r core.Retriever) Option { return func(a *Agent) { a.Retriever = r } }

// WithMemoryWriteJudge overrides the default gate on memory writes.
func WithMemoryWriteJudge(j harness.MemoryWriteJudge) Option {
	return func(a *Agent) { a.MemoryWriteJudge = j }
}

// WithMemory enables M5 short-term and long-term memory.
func WithMemory(m core.MemoryStore) Option { return func(a *Agent) { a.Memory = m } }

// WithCheckpoint enables M12 state persistence for crash recovery.
func WithCheckpoint(c core.CheckpointStore) Option { return func(a *Agent) { a.Checkpoint = c } }

// WithCheckpointFailurePolicy makes a failed checkpoint write fail the run.
// The default is best-effort, where the failure is logged and the run continues.
func WithCheckpointFailurePolicy(p harness.CheckpointFailurePolicy) Option {
	return func(a *Agent) { a.CheckpointFailurePolicy = p }
}

// WithEffectFailurePolicy makes a failed safety effect (budget, output guard,
// verifier, memory write) fail the run. The default is best-effort.
func WithEffectFailurePolicy(p harness.EffectFailurePolicy) Option {
	return func(a *Agent) { a.EffectFailurePolicy = p }
}

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
//
// This is the single function external callers (schedulers, API handlers) invoke.
func (a *Agent) Run(ctx context.Context, sessionID string, userInput string) (*harness.RunResult, error) {
	loop, stop, err := a.buildLoop(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	return loop.Run(ctx, sessionID, userInput)
}

// Resume continues a session from its latest checkpoint instead of restarting it.
// The agent must have been configured with a checkpoint store.
func (a *Agent) Resume(ctx context.Context, sessionID string) (*harness.RunResult, error) {
	loop, stop, err := a.buildLoop(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	return loop.Resume(ctx, sessionID)
}

// buildLoop assembles the ToolRegistry, ToolRouter and AgentLoop, injects the
// optional modules and starts MCP servers when configured.
//
// The returned function releases whatever was started and must be deferred by
// the caller: stopping MCP before the loop runs would leave the loop without the
// tools that were just bridged in.
func (a *Agent) buildLoop(ctx context.Context) (*harness.AgentLoop, func(), error) {
	// 1. Build ToolRegistry with all built-in handlers.
	registry := workers.NewToolRegistry()
	cfg := workers.HandlerConfig{
		Mode:      workers.HandlerModeMock, // TODO: make configurable
		Workspace: "/tmp/forge-workspace",
		// The retriever reaches the agent through knowledge.search only;
		// retrieval stays agentic rather than being pushed into every prompt.
		Retriever: a.Retriever,
	}
	if err := workers.RegisterAll(registry, cfg); err != nil {
		return nil, nil, fmt.Errorf("register tools: %w", err)
	}

	stop := func() {}

	// 2. Start MCP servers and bridge their tools into the registry.
	if a.MCP != nil {
		if err := a.MCP.Start(ctx); err != nil {
			return nil, nil, fmt.Errorf("start MCP: %w", err)
		}
		stop = func() { _ = a.MCP.Stop() }

		if mgr, ok := a.MCP.(*mcp.Manager); ok {
			bridge := mcp.NewBridge(mgr, registry)
			if _, err := bridge.Sync(ctx); err != nil {
				stop()
				return nil, nil, fmt.Errorf("sync MCP tools: %w", err)
			}
		}
	}

	// 3. Build the AgentLoop.
	router := harness.NewToolRouter(registry)
	loopCfg := harness.LoopConfig{
		MaxSteps: a.Config.MaxSteps,
		// There is exactly one context-budget default. This used to be hardcoded
		// to 128000 while harness.DefaultMaxContextTokens (and every caller that
		// passes a budget, e.g. workers/ai and workers/review) used 100000.
		MaxContextTokens: harness.DefaultMaxContextTokens,
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
	if a.CheckpointFailurePolicy != "" {
		loop.SetCheckpointFailurePolicy(a.CheckpointFailurePolicy)
	}
	if a.EffectFailurePolicy != "" {
		loop.SetEffectFailurePolicy(a.EffectFailurePolicy)
	}
	if a.Memory != nil {
		loop.SetMemory(a.Memory)
	}
	if a.Verifier != nil {
		loop.SetVerifier(a.Verifier)
	}
	if a.MemoryWriteJudge != nil {
		loop.SetMemoryWriteJudge(a.MemoryWriteJudge)
	}

	return loop, stop, nil
}
