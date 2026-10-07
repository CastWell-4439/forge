package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/castwell/forge/internal/agent/core"
)

// Delegation (N5).
//
// A delegation runs a CHILD loop in its own session and its own context, then
// reports back according to the configured report shape. The isolation is the
// feature: a subtask that needs to read twenty files to answer one question
// would otherwise spend the parent's entire window on material the parent never
// needed to see.
//
// Three things this file is careful about:
//
//   - Depth is carried per RUN, not per process. The same Agent instance can
//     serve a root run and a nested one, so a process-global depth counter would
//     be wrong the moment two runs overlap. It travels in the context.
//   - The child is built by a factory the caller supplies. This package must not
//     know how an agent is assembled (that would point the dependency the wrong
//     way), so it asks for "a loop configured like the parent, for this child".
//   - Failures are results, not panics or silent zeros. A child that stopped
//     early comes back with its Reason set, and the caller turns that into an
//     error the model can read.

// subagentDepthKey carries the current delegation depth in a context.
type subagentDepthKey struct{}

// WithSubagentDepth returns a context that reports the given delegation depth.
// The top-level run uses 0.
func WithSubagentDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, subagentDepthKey{}, depth)
}

// SubagentDepthFrom reports the delegation depth recorded in a context.
func SubagentDepthFrom(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if d, ok := ctx.Value(subagentDepthKey{}).(int); ok {
		return d
	}
	return 0
}

// ChildLoopFactory builds a loop for a delegated child.
//
// It receives the child's session id (so the child's persistence is its own)
// and the narrowed tool set (empty = inherit). It returns the loop and a
// release function, mirroring how the parent's own assembly works: whatever the
// child starts must be stopped when the child is done.
type ChildLoopFactory func(ctx context.Context, childSessionID string, tools []string) (*AgentLoop, func(), error)

// SubagentRunnerConfig configures the runner.
type SubagentRunnerConfig struct {
	// Factory builds a child loop. Required.
	Factory ChildLoopFactory
	// Config is the resolved delegation configuration.
	Config core.SubagentConfig
	// NewChildID mints a child session id. Injected so tests can make it
	// deterministic; the default derives it from the parent session.
	NewChildID func(parentSessionID string, seq int) string
}

// SubagentRunnerImpl performs delegations for one parent loop.
type SubagentRunnerImpl struct {
	cfg SubagentRunnerConfig
	// mu guards the sequence counter and the live-children table.
	mu sync.Mutex
	// seq numbers children within this parent, so ids are stable and ordered.
	seq int
	// sem bounds concurrent children. It is created once (per parent loop) so
	// the limit applies across delegations rather than per call.
	sem chan struct{}
}

// NewSubagentRunner builds a runner. It returns nil when delegation is off, so
// callers can treat "no runner" and "disabled" as the same thing.
func NewSubagentRunner(cfg SubagentRunnerConfig) *SubagentRunnerImpl {
	cfg.Config = core.NormalizeSubagentConfig(cfg.Config)
	if !cfg.Config.Mode.Enabled() || cfg.Factory == nil {
		return nil
	}
	if cfg.NewChildID == nil {
		cfg.NewChildID = defaultChildID
	}
	concurrency := cfg.Config.Limits.MaxConcurrent
	if concurrency <= 0 {
		concurrency = core.DefaultSubagentMaxConcurrent
	}
	return &SubagentRunnerImpl{
		cfg: cfg,
		sem: make(chan struct{}, concurrency),
	}
}

// defaultChildID derives a child session id from its parent.
//
// The parent id is kept as a prefix so a child's lineage is readable from its
// id alone: given a session id in a log, "who delegated this" is answerable
// without consulting a registry that may no longer exist.
func defaultChildID(parentSessionID string, seq int) string {
	parent := strings.TrimSpace(parentSessionID)
	if parent == "" {
		parent = "session"
	}
	return fmt.Sprintf("%s-sub-%d", parent, seq)
}

// checkDepth reports whether a caller at the given depth may delegate.
//
// Depth is checked against the CALLER's depth, not the child's: MaxDepth 1 means
// "one level of delegation", so a depth-0 caller may delegate and a depth-1
// caller may not. Expressed that way the rule reads the same as the config.
func (r *SubagentRunnerImpl) checkDepth(callerDepth int) error {
	if r == nil {
		return fmt.Errorf("delegation is not available")
	}
	if callerDepth >= r.cfg.Config.Limits.MaxDepth {
		return fmt.Errorf(
			"delegation depth limit reached (depth %d, max %d): this agent may not delegate further",
			callerDepth, r.cfg.Config.Limits.MaxDepth)
	}
	return nil
}

// RunSubagent performs one delegation.
func (r *SubagentRunnerImpl) RunSubagent(ctx context.Context, req core.SubagentRequest) (*core.SubagentResult, error) {
	if r == nil {
		return nil, fmt.Errorf("delegation is not available")
	}

	// Depth is checked against the CALLER's depth, read from the request (which
	// the tool filled from the context).
	if err := r.checkDepth(req.Depth); err != nil {
		return nil, err
	}

	continuing := req.ChildID != ""
	if continuing && !r.cfg.Config.Mode.Continuable() {
		return nil, fmt.Errorf("continuing a child requires continuable mode")
	}

	childID := req.ChildID
	if childID == "" {
		r.mu.Lock()
		r.seq++
		seq := r.seq
		r.mu.Unlock()
		childID = r.cfg.NewChildID(parentSessionFromContext(ctx), seq)
	}

	// Concurrency is bounded by the semaphore. A batch of subagent calls in one
	// step therefore runs at most MaxConcurrent at a time, which is the
	// difference between "fan out" and "exhaust the provider".
	if r.sem != nil {
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	loop, release, err := r.cfg.Factory(ctx, childID, r.cfg.Config.Tools)
	if err != nil {
		return nil, fmt.Errorf("start child %s: %w", childID, err)
	}
	defer release()

	// The child runs at depth+1, which is what stops a child from delegating
	// forever when the limit allows more than one level.
	childCtx := WithSubagentDepth(ctx, req.Depth+1)

	var result *RunResult
	if continuing {
		// Continue an existing child: its own session holds the history, and
		// this new task is appended to it. That is the difference between
		// "delegate again" and "start over".
		result, err = loop.Continue(childCtx, childID, req.Task)
	} else {
		result, err = loop.Run(childCtx, childID, req.Task)
	}
	if err != nil {
		return nil, fmt.Errorf("child %s: %w", childID, err)
	}

	out := &core.SubagentResult{
		ChildID: childID,
		Answer:  result.Answer,
		Reason:  result.Reason,
	}
	if result.Reason == "paused" {
		out.Paused = true
		out.PauseReason = result.PauseReason
	}

	// The report shape decides how much of the child's work crosses back. Under
	// Answer the steps are dropped entirely — that omission is the isolation,
	// not an oversight.
	switch r.cfg.Config.Report {
	case core.SubagentReportAnswer:
		// nothing: the answer is the whole result
	case core.SubagentReportSteps:
		out.Steps = childSteps(result.Steps, true)
	default: // summary
		out.Steps = childSteps(result.Steps, false)
	}

	return out, nil
}

// SubagentDepth reports the caller's depth, so the tool can pass it in.
func (r *SubagentRunnerImpl) SubagentDepth(ctx context.Context) int {
	return SubagentDepthFrom(ctx)
}

// childSteps converts a child's step records into the reported trace.
//
// includeResults decides whether tool output travels: under "steps" the parent
// asked to see everything (including how big the output was), under "summary"
// it asked only for the shape of the work.
func childSteps(steps []StepRecord, includeResults bool) []core.SubagentStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]core.SubagentStep, 0, len(steps))
	for _, s := range steps {
		step := core.SubagentStep{
			Step:    s.Step,
			Thought: s.Thought,
		}
		// Action is nil for the terminal step (the model answered instead of
		// calling a tool), so it is checked rather than assumed. A nil
		// dereference here would take down the parent run because a child
		// finished normally.
		if s.Action != nil {
			step.Action = s.Action.Name
		}
		if includeResults && s.Result != nil {
			step.Result = s.Result.Output
			if step.Result == "" && s.Result.Error != "" {
				step.Result = "error: " + s.Result.Error
			}
		}
		out = append(out, step)
	}
	return out
}

// parentSessionKey carries the parent's session id, so a child id can name its
// parent without the runner being told separately.
type parentSessionKey struct{}

// WithParentSession records the session id a delegation tree belongs to.
func WithParentSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, parentSessionKey{}, sessionID)
}

// parentSessionFromContext reports the recorded parent session id.
func parentSessionFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(parentSessionKey{}).(string); ok {
		return s
	}
	return ""
}

// Continue runs another round on an existing session.
//
// It differs from Resume in one way that matters here: Resume re-enters a run
// that stopped, with no new input; Continue appends a NEW user turn to a session
// that already finished. That is what "give the child more work" means, and it
// is why the delegation feature needs it rather than reusing Resume.
func (l *AgentLoop) Continue(ctx context.Context, sessionID, userInput string) (*RunResult, error) {
	return l.run(ctx, sessionID, userInput, runContinue)
}
