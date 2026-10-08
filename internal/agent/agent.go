// Package agent is the top-level entry point for the Agent layer.
// It assembles all sub-packages (planning, session, tools, workers)
// and optional enhancement modules via functional options.
package agent

import (
	"context"
	"fmt"
	"os"
	"sync"

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
	// ToolOutputGuard screens untrusted tool output before it is shown to the
	// model (nil = pass through, the historical behaviour).
	ToolOutputGuard core.InputGuard
	// NativeTools requests provider-native function calling (tools/tool_calls
	// with real parameter schemas) instead of the prompt-encoded JSON
	// convention. It is a request: an endpoint that refuses the tools field
	// downgrades to the prompt path with one warning.
	NativeTools bool
	// Context-window tuning (N2c): water-line reminders as fractions of the
	// window (0 = defaults 0.70/0.90; negative remind disables), how many
	// trailing messages survive a compaction (0 = default 4), and the fraction
	// of the budget a compaction aims for (0 = default 0.60).
	ContextRemindAt      float64
	ContextUrgentAt      float64
	CompactKeepMessages  int
	ContextCompactTarget float64
	// OutputReserve is how many tokens to hold back for the model's reply (N3).
	// Zero means no reservation.
	OutputReserve int
	// Subagent configures delegation (N5). The zero value is Mode "off": no
	// subagent tool is registered and nothing about this agent changes.
	Subagent core.SubagentConfig
	// NoProgressThreshold is how many consecutive identical tool calls end a run
	// (0 = the harness default, negative = disabled).
	NoProgressThreshold int
	// ToolSlim is the S2 slimming policy: which head/tail split applies to which
	// tool (zero value = the historical profile).
	ToolSlim core.ToolSlimConfig
	// calibration remembers the token-estimate ratio per model ACROSS runs. The
	// ratio describes the provider's tokenizer, not the conversation, so
	// re-learning it every run meant a short run never learned it at all. It is
	// created on first use when nil; it is per-Agent (not package-level) because
	// two agents may point at different endpoints.
	calibration *harness.CalibrationStore
	// calibrationOnce guards lazy creation of the store.
	calibrationOnce sync.Once
	// Authority is the ceiling runs may act under (L0..L4). Empty means
	// core.DefaultAuthority. A deployment-level decision, read at assembly.
	Authority core.Authority
	// VisibleTools caps the tool descriptions injected into the prompt (C3).
	// 0 = loop default; negative = no filtering.
	VisibleTools int
	Budget       core.BudgetChecker
	Retriever    core.Retriever
	Memory       core.MemoryStore
	Checkpoint   core.CheckpointStore
	Journal      harness.Journal
	MCP          core.MCPManager
	Verifier     core.Verifier

	// CheckpointFailurePolicy decides whether a failed checkpoint write is fatal.
	// Empty keeps the best-effort default.
	CheckpointFailurePolicy harness.CheckpointFailurePolicy

	// EffectFailurePolicy decides whether a failed budget/guard/verifier/memory
	// write is fatal. Empty keeps the best-effort default.
	EffectFailurePolicy harness.EffectFailurePolicy

	// MemoryWriteJudge overrides the default gate that decides whether a
	// finished run is worth remembering.
	MemoryWriteJudge harness.MemoryWriteJudge

	// LessonSource is the read-only lessons channel (F3): lessons distilled by
	// the control plane from finished runs. Nil = no lesson recall.
	LessonSource core.LessonSource
	// LessonFilter overrides the default read-side gate (deterministic, with
	// the self-feedback guard). Nil = the default.
	LessonFilter harness.LessonFilter

	// HandlerMode selects the tool execution mode for this agent. Empty means
	// real — the production default; tests and demos pass mock explicitly.
	HandlerMode workers.HandlerMode
	// Workspace is the base directory for file/shell tools. Empty means
	// ".forge-workspace"; the directory is created on demand at loop build.
	Workspace string

	// toolConfigApply are assembly-level hooks that fill HandlerConfig
	// fields derived from the environment (data source, web backends, ...).
	// The agent package stays free of env reads; whoever assembles it owns
	// those settings — same injection philosophy as AskUser/Retriever.
	toolConfigApply []func(*workers.HandlerConfig)
}

// Option configures an optional module on the Agent.
type Option func(*Agent)

// WithInputGuard enables M6 input safety checks.
func WithInputGuard(g core.InputGuard) Option { return func(a *Agent) { a.InputGuard = g } }

// WithOutputGuard enables M6 output content filtering.
func WithOutputGuard(g core.OutputGuard) Option { return func(a *Agent) { a.OutputGuard = g } }

// WithToolOutputGuard screens untrusted tool output before it reaches the
// model — the indirect-injection path (a web page, file or MCP reply telling
// the model to ignore its instructions). It takes an InputGuard because the
// text is entering the model, not leaving it.
func WithToolOutputGuard(g core.InputGuard) Option { return func(a *Agent) { a.ToolOutputGuard = g } }

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

// WithLessonSource enables F3 lesson recall: the agent starts a run with what
// the control plane learned from earlier runs. Read-only — the agent never
// writes lessons (see core.LessonSource).
func WithLessonSource(s core.LessonSource) Option {
	return func(a *Agent) { a.LessonSource = s }
}

// WithLessonFilter overrides the default read-side gate on recalled lessons.
func WithLessonFilter(f harness.LessonFilter) Option {
	return func(a *Agent) { a.LessonFilter = f }
}

// WithCheckpoint enables M12 state persistence for crash recovery.
func WithCheckpoint(c core.CheckpointStore) Option { return func(a *Agent) { a.Checkpoint = c } }

// WithJournal enables the run journal (D-12): append-only events that record
// what a run did, back the span projection, and let a resume rebuild state
// when the checkpoint cache is missing.
func WithJournal(j harness.Journal) Option { return func(a *Agent) { a.Journal = j } }

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

// WithHandlerMode selects the tool execution mode: real (the default — tools
// actually run) or mock (canned results, for tests and demos).
func WithHandlerMode(m workers.HandlerMode) Option { return func(a *Agent) { a.HandlerMode = m } }

// WithWorkspace sets the base directory for file/shell tools. It is created
// on demand when the loop is built.
func WithWorkspace(dir string) Option { return func(a *Agent) { a.Workspace = dir } }

// WithToolConfig applies assembly-level handler settings (data source, web
// search backend, fetch policy, ...) every time the loop is built.
func WithToolConfig(apply func(*workers.HandlerConfig)) Option {
	return func(a *Agent) { a.toolConfigApply = append(a.toolConfigApply, apply) }
}

// WithNativeTools enables provider-native function calling for this agent.
// The wiring decides (env at assembly); the loop treats it as a request the
// endpoint may refuse, degrading to the prompt path with one warning.
func WithNativeTools(enabled bool) Option {
	return func(a *Agent) { a.NativeTools = enabled }
}

// WithAuthority sets the ceiling this agent's runs may act under (L0..L4).
// It is a deployment-level decision: a workflow cannot raise it, which is what
// keeps a pipeline from granting itself permission to delete. Empty means
// core.DefaultAuthority (L2, read-only without asking).
func WithAuthority(authority core.Authority) Option {
	return func(a *Agent) { a.Authority = authority }
}

// WithVisibleTools caps how many tool descriptions the prompt carries (C3).
// 0 means the loop's default; negative disables filtering (every tool listed,
// the pre-C3 behaviour). The native tools path ignores this: there the list is
// an API parameter, so nothing needs trimming.
func WithVisibleTools(limit int) Option {
	return func(a *Agent) { a.VisibleTools = limit }
}

// WithContextTuning sets the water-line reminder thresholds (fractions of the
// window; 0 = defaults; remind < 0 disables), the compaction keep-count and the
// compaction target fraction (S1/S4). The wiring reads env; the loop only sees
// numbers.
func WithContextTuning(remind, urgent float64, keepMessages int, compactTarget float64) Option {
	return func(a *Agent) {
		a.ContextRemindAt = remind
		a.ContextUrgentAt = urgent
		a.CompactKeepMessages = keepMessages
		a.ContextCompactTarget = compactTarget
	}
}

// WithOutputReserve sets how many tokens are held back for the model's reply
// (N3). The context budget covers the whole window, so without a reservation
// the input may fill it and the generation that follows overflows — the failure
// this option exists to prevent. Zero means no reservation.
func WithOutputReserve(tokens int) Option {
	return func(a *Agent) { a.OutputReserve = tokens }
}

// WithNoProgressThreshold sets how many consecutive identical tool calls end a
// run (N5 follow-up). 0 means the harness default; negative disables the streak
// stop. It is a deployment decision because the right number depends on the
// work: exploring a question may legitimately re-read the same file, while a
// batch pipeline repeating a call three times is already stuck.
func WithNoProgressThreshold(n int) Option {
	return func(a *Agent) { a.NoProgressThreshold = n }
}

// WithToolSlim sets the S2 slimming policy: which head/tail split applies to
// which tool. The zero value reproduces the historical fixed profile, so an
// unconfigured deployment is byte-for-byte unchanged.
func WithToolSlim(cfg core.ToolSlimConfig) Option {
	return func(a *Agent) { a.ToolSlim = cfg.Normalize() }
}

// WithCalibrationStore attaches a shared calibration store.
//
// Passing the same store to several agents makes them learn from each other,
// which is right when they share an endpoint and wrong when they do not — the
// ratio is keyed by model, so different models stay separate either way.
func WithCalibrationStore(store *harness.CalibrationStore) Option {
	return func(a *Agent) { a.calibration = store }
}

// calibrationStore returns the store, creating one on first use.
func (a *Agent) calibrationStore() *harness.CalibrationStore {
	a.calibrationOnce.Do(func() {
		if a.calibration == nil {
			a.calibration = harness.NewCalibrationStore()
		}
	})
	return a.calibration
}

// WithSubagent configures delegation (N5).
//
// The zero value disables it: a deployment that does not ask for subagents gets
// no subagent tool, no schema cost and no prompt cost. That default is the same
// choice the webhook listener and the Kueue queue make — not configuring a
// capability must not enable it.
func WithSubagent(cfg core.SubagentConfig) Option {
	return func(a *Agent) { a.Subagent = core.NormalizeSubagentConfig(cfg) }
}

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
	mode := a.HandlerMode
	if mode == "" {
		// Real is the production default: an agent with tools is expected to
		// run them. Tests and demos pass WithHandlerMode(mock) explicitly —
		// this used to be hardcoded to mock, which is exactly why the golden
		// tool set never actually executed anything.
		mode = workers.HandlerModeReal
	}
	workspace := a.Workspace
	if workspace == "" {
		workspace = ".forge-workspace"
	}
	// Real handlers stat the workspace before every file/shell call and
	// nobody else creates it — without this, real mode would fail its very
	// first tool call with "directory does not exist".
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create workspace %s: %w", workspace, err)
	}
	cfg := workers.HandlerConfig{
		Mode:      mode,
		Workspace: workspace,
		// The retriever reaches the agent through knowledge.search only;
		// retrieval stays agentic rather than being pushed into every prompt.
		Retriever: a.Retriever,
	}
	for _, apply := range a.toolConfigApply {
		apply(&cfg)
	}
	if err := workers.RegisterAll(registry, cfg); err != nil {
		return nil, nil, fmt.Errorf("register tools: %w", err)
	}

	// 1b. Delegation (N5): register the subagent tool when the deployment asks
	// for it. The child loop is built by the SAME assembly path as the parent
	// (see childFactory below), so a child gets the same tools, guards and MCP
	// bridge — a child that silently lacked them would be a different agent, and
	// the model would have no way to know.
	//
	// The runner is created here rather than per run because its concurrency
	// limit is a property of this parent: MaxConcurrent bounds how many children
	// ONE parent runs at once, which is the resource the limit protects.
	subCfg := core.NormalizeSubagentConfig(a.Subagent)
	var subRunner *harness.SubagentRunnerImpl
	if subCfg.Mode.Enabled() {
		subRunner = harness.NewSubagentRunner(harness.SubagentRunnerConfig{
			Config:  subCfg,
			Factory: a.childFactory(registry, cfg, subCfg),
		})
		if subRunner != nil {
			handler := workers.NewSubagentHandler(subRunner, subCfg)
			if err := registry.Register(workers.SubagentDef(subCfg), handler.Handle); err != nil {
				return nil, nil, fmt.Errorf("register %s: %w", workers.SubagentName, err)
			}
		}
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
	loopCfg := a.baseLoopConfig()
	loop := harness.NewAgentLoop(a.LLM, router, loopCfg)

	// 3b. Seed the token estimate from what previous runs learned. The ratio
	// belongs to the provider+model, so a short run inherits it instead of
	// starting from the default guess it would never have time to correct.
	loop.SetCalibration(a.calibrationStore())

	// 4. Inject optional modules.
	a.applyLoopDeps(loop)

	return loop, stop, nil
}

// baseLoopConfig builds the loop configuration shared by a parent and its
// children (N5).
//
// It is one function so the two cannot drift: a child assembled with a
// hand-copied config would silently lose whichever knob was added last, and
// nothing would report it.
func (a *Agent) baseLoopConfig() harness.LoopConfig {
	return harness.LoopConfig{
		MaxSteps: a.Config.MaxSteps,
		// There is exactly one context-budget default. This used to be hardcoded
		// to 128000 while harness.DefaultMaxContextTokens (and every caller that
		// passes a budget, e.g. workers/ai and workers/review) used 100000.
		MaxContextTokens: harness.DefaultMaxContextTokens,
		// 0 picks harness.DefaultNoProgressThreshold — an agent stuck repeating
		// itself stops honestly instead of burning the whole step budget.
		NoProgressThreshold:  a.NoProgressThreshold,
		ToolSlim:             a.ToolSlim,
		NativeTools:          a.NativeTools,
		ContextRemindAt:      a.ContextRemindAt,
		ContextUrgentAt:      a.ContextUrgentAt,
		CompactKeepMessages:  a.CompactKeepMessages,
		ContextCompactTarget: a.ContextCompactTarget,
		OutputReserve:        a.OutputReserve,
		Authority:            a.Authority,
		VisibleTools:         a.VisibleTools,
	}
}

// applyLoopDeps installs the optional modules onto a loop.
//
// Shared with children for the same reason as baseLoopConfig: the guards and
// stores a parent has are the ones a child must have too. A child that skipped
// the output guard would be a hole in the deployment's screening, and the
// parent could not tell.
func (a *Agent) applyLoopDeps(loop *harness.AgentLoop) {
	if a.InputGuard != nil {
		loop.SetInputGuard(a.InputGuard)
	}
	if a.OutputGuard != nil {
		loop.SetOutputGuard(a.OutputGuard)
	}
	if a.ToolOutputGuard != nil {
		loop.SetToolOutputGuard(a.ToolOutputGuard)
	}
	if a.Budget != nil {
		loop.SetBudget(a.Budget)
	}
	if a.Checkpoint != nil {
		loop.SetCheckpoint(a.Checkpoint)
	}
	if a.Journal != nil {
		loop.SetJournal(a.Journal)
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
	if a.LessonSource != nil {
		loop.SetLessonSource(a.LessonSource)
	}
	if a.LessonFilter != nil {
		loop.SetLessonFilter(a.LessonFilter)
	}
}
