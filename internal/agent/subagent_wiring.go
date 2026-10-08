package agent

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/harness"
	"github.com/castwell/forge/internal/agent/workers"
)

// Child assembly (N5).
//
// A delegated child is built by the same code path as its parent: same handler
// mode, same workspace, same guards, same MCP bridge. That is deliberate. A
// child assembled by a separate, simpler path would be a different agent — it
// would miss the permission gate, or the output guard, or the tool-narrowing —
// and the parent's model, which cannot see inside the child, would have no way
// to notice.
//
// Two things are NOT inherited, and both are the point of delegating:
//
//   - The conversation. The child starts from its own session, so the parent's
//     window is untouched by whatever the child reads and reasons about.
//   - The step budget. A child gets a bounded share (see DefaultChildSteps), so
//     one subtask cannot spend everything the parent has left.

// childFactory builds the ChildLoopFactory for this agent.
func (a *Agent) childFactory(
	parentRegistry *workers.ToolRegistry,
	parentCfg workers.HandlerConfig,
	subCfg core.SubagentConfig,
) harness.ChildLoopFactory {
	return func(ctx context.Context, childSessionID string, tools []string) (*harness.AgentLoop, func(), error) {
		registry := workers.NewToolRegistry()

		// The child's handler config is the parent's, with one change: the
		// subagent tool is NOT registered again here unless the depth limit
		// allows it. Registering it unconditionally would make the depth check
		// the only guard, and a child that cannot delegate should not be shown a
		// delegation tool it will always be refused.
		//
		// The depth is read from the context (the runner sets it), so this
		// factory knows whether the child may itself delegate.
		childDepth := harness.SubagentDepthFrom(ctx)
		childMayDelegate := childDepth < subCfg.Limits.MaxDepth

		cfg := parentCfg
		if err := workers.RegisterAll(registry, cfg); err != nil {
			return nil, nil, fmt.Errorf("register child tools: %w", err)
		}

		if childMayDelegate {
			// The child gets its own runner so its concurrency budget is its own
			// (one parent's fan-out must not consume another's).
			childRunner := harness.NewSubagentRunner(harness.SubagentRunnerConfig{
				Config:  subCfg,
				Factory: a.childFactory(registry, cfg, subCfg),
			})
			if childRunner != nil {
				handler := workers.NewSubagentHandler(childRunner, subCfg)
				if err := registry.Register(workers.SubagentDef(subCfg), handler.Handle); err != nil {
					return nil, nil, fmt.Errorf("register child %s: %w", workers.SubagentName, err)
				}
			}
		}

		// Tool narrowing: the deployment may hand a child a subset. Names that
		// do not exist are refused rather than ignored — a typo in a tool list
		// would otherwise silently leave the child MORE capable than intended,
		// which is the wrong direction to fail.
		if len(tools) > 0 {
			if err := narrowRegistry(registry, tools); err != nil {
				return nil, nil, err
			}
		}

		loopCfg := a.loopConfigFor(registry)
		router := harness.NewToolRouter(registry)
		child := harness.NewAgentLoop(a.LLM, router, loopCfg)
		a.applyLoopDeps(child)

		// The child talks to the same provider and model as its parent, so the
		// calibration ratio applies to it too — and sharing the store means a
		// child's observations also improve the parent's next run. A child with
		// its own store would re-learn the same ratio from scratch, which is the
		// waste the store was introduced to remove.
		child.SetCalibration(a.calibrationStore())

		return child, func() {}, nil
	}
}

// narrowRegistry removes every tool not named in keep.
//
// Removal rather than a filter at call time: a tool the child must not use
// should not appear in its prompt at all, both because a description costs
// tokens and because a visible tool that always fails is worse feedback than an
// absent one.
func narrowRegistry(registry *workers.ToolRegistry, keep []string) error {
	wanted := make(map[string]bool, len(keep))
	for _, name := range keep {
		wanted[name] = true
	}

	// Check the requested names first: an unknown name means the deployment is
	// wrong, and it must be reported before anything is removed.
	available := map[string]bool{}
	for _, def := range registry.ListTools() {
		available[def.Name] = true
	}
	for name := range wanted {
		if !available[name] {
			return fmt.Errorf("subagent tool filter names unknown tool %q", name)
		}
	}

	for _, def := range registry.ListTools() {
		if !wanted[def.Name] {
			registry.Unregister(def.Name)
		}
	}
	return nil
}

// loopConfigFor builds a LoopConfig for a child from the parent's settings.
//
// It reuses the parent's tuning (water lines, compaction, authority) because a
// child is the same kind of agent; it lowers only the step budget, which is the
// one resource a child must not be able to drain.
func (a *Agent) loopConfigFor(registry *workers.ToolRegistry) harness.LoopConfig {
	cfg := a.baseLoopConfig()
	cfg.MaxSteps = core.DefaultChildSteps(a.Config.MaxSteps)
	return cfg
}
