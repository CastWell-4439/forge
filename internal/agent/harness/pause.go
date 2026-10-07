package harness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// The human-in-the-loop and permission gates (N4 + the risk gate).
//
// Two different reasons a run stops to ask a person, kept apart because the
// answers differ:
//
//   - AUTHORITY: this run may not do that at all. The fix is a different
//     authority (a supervisor), or a different plan.
//   - PAUSE: the run is allowed to continue, but nobody has decided whether it
//     should. The fix is a decision.
//
// Both end the same way — Reason "paused", saved and resumable — because from
// the outside "waiting for a human" is one state. What they keep distinct is
// the reason text, which is what the person actually reads.

// PauseToolName is the pause tool as the loop sees it. Declared here rather
// than imported from workers: harness must not depend on the tool package
// (dependency direction), and the name is part of the tool protocol either way.
const PauseToolName = "agent.pause"

// QueryToolName is the query tool as the loop sees it.
const QueryToolName = "agent.query"

// ToolSearchToolName is the tool-discovery tool as the loop sees it.
const ToolSearchToolName = "tool.search"

// isControlTool reports whether a tool name is intercepted by the loop rather
// than dispatched to a handler.
func isControlTool(name string) bool {
	switch name {
	case PauseToolName, QueryToolName, ToolSearchToolName:
		return true
	default:
		return false
	}
}

// pauseReason reads the 'reason' parameter, trimmed.
func pauseReason(params map[string]any) string {
	if params == nil {
		return ""
	}
	reason, _ := params["reason"].(string)
	return strings.TrimSpace(reason)
}

// pauseRun ends the run in the one resumable way: journal the pause, save the
// checkpoint, and report Reason "paused".
//
// Order matters. The journal records the intent first (so a rebuild can see
// that this run paused rather than crashed), then the checkpoint stores the
// exact position, and only then does the run return. A pause that is not
// journaled is indistinguishable from a crash; a pause without a checkpoint
// cannot be resumed.
func (l *AgentLoop) pauseRun(ctx context.Context, sessionID string, step int, messages []core.Message,
	ledger []core.ToolCallRecord, reason string) (*RunResult, error) {

	// The assistant's request is part of the record: the human needs to see
	// WHAT the run wanted to do, not just that it stopped.
	messages = append(messages, core.Message{
		Role:    "assistant",
		Content: fmt.Sprintf("[pause requested]: %s", reason),
	})

	if jerr := l.journalAppend(ctx, RunEvent{
		RunID: sessionID,
		Type:  EventRunPaused,
		Step:  step,
		TS:    time.Now().UTC(),
		Data:  map[string]any{"reason": reason, "turns": messages},
	}); jerr != nil {
		return nil, jerr
	}
	if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
		return nil, err
	}
	l.emitRunEnded(ctx, sessionID, step, "paused", reason)

	return &RunResult{
		Answer:      fmt.Sprintf("Paused for a human decision: %s", reason),
		Steps:       nil,
		Reason:      "paused",
		PauseReason: reason,
	}, nil
}

// authority is the ceiling this run operates under. A zero value means "not
// configured", which resolves to core.DefaultAuthority (L2, read-only without
// asking) rather than L0: an unconfigured deployment should still be able to
// read files, and anything more permissive must be asked for explicitly.
func (l *AgentLoop) authority() core.Authority {
	if l.config.Authority == "" {
		return core.DefaultAuthority
	}
	return core.NormalizeAuthority(string(l.config.Authority))
}

// checkToolAuthority applies the permission gate before a tool runs.
//
// It returns (nil, false) when the call may proceed. When it returns a pause
// result, the caller must return it: the run stops here, saved and resumable,
// with the reason naming the tool and the authority it would have needed. The
// message is written for the person releasing the run, not for the model —
// they are the ones who must act.
func (l *AgentLoop) checkToolAuthority(toolName string) (core.EffectDecision, bool) {
	// Meta operations are the loop's own bookkeeping: pausing, querying and
	// window management must never be blocked by the gate, or a run could be
	// unable to ask for the permission it lacks.
	if isContextTool(toolName) || isControlTool(toolName) {
		return core.EffectDecision{Allowed: true}, true
	}

	var def *core.ToolDef
	if l.router != nil && l.router.registry != nil {
		def = l.router.registry.GetTool(toolName)
	}
	if def == nil {
		// Not a known tool. That is a routing error the MODEL can fix (it named
		// the wrong tool), not a permission question a human should be woken
		// for: the router below will report "unknown tool" as an observation.
		// Gating here would turn every typo into a pause.
		return core.EffectDecision{Allowed: true}, true
	}

	decision := core.CheckToolEffect(l.authority(), l.config.TaskEffect, def)
	return decision, decision.Allowed
}

// authorityPauseResult builds the run result for a call the gate refused.
//
// The distinction the message carries: whether a human COULD release this
// (the required authority is reachable) or whether the task itself asks for
// something this deployment never grants. Both pause, but only the first is
// worth waking someone up for.
func (l *AgentLoop) authorityPauseResult(toolName string, decision core.EffectDecision) (*RunResult, error) {
	held := l.authority()
	releasable := core.AuthorityCanApprove(core.AuthorityL4, decision.Required)

	reason := fmt.Sprintf("tool %q needs authority %s (effect: %s), but this run holds %s",
		toolName, decision.Required, decision.Effect, held)
	if !releasable {
		reason += "; this deployment never grants that authority, so the plan must change"
	} else {
		reason += "; approve to continue, or ask for a plan that stays within the granted authority"
	}
	return &RunResult{
		Answer:      "Paused for approval: " + reason,
		Reason:      "paused",
		PauseReason: reason,
	}, nil
}
