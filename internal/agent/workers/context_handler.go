package workers

import (
	"context"
	"fmt"
)

// Context-window introspection tools (N2c).
//
// Three tools, one per operation, following the smallest-isolation rule the
// author set for this surface (and matching how `file.*` is six independent
// tools rather than one multi-action tool):
//
//	context.remaining()      how many tokens are left in this window
//	context.compact()        start a new window; environment state untouched
//	context.recall(query)    search the archived conversation (the journal)
//
// They are META operations on the loop's own state, not capabilities that
// touch the world: the loop intercepts them before the router (see
// harness.runContextTool). The handlers registered here exist so the registry
// shape stays uniform, and they deliberately FAIL — a direct call would mean
// the interception did not happen, and silently doing nothing would hide
// that. The error names the contract.

// ContextToolsFunc is the loop-side implementation, injected from assembly.
//
// It returns the textual result for the named operation. An unknown action is
// an error, not an empty string: the caller must be able to tell "no answer"
// from "no such operation".
type ContextToolsFunc func(ctx context.Context, action string, params map[string]any) (string, error)

// Context tool names. Kept as constants so the loop, the definitions and the
// golden-list test cannot drift apart by typo.
const (
	ToolContextRemaining = "context.remaining"
	ToolContextCompact   = "context.compact"
	ToolContextRecall    = "context.recall"
)

// ContextRemainingDef reports the tokens left in the current window.
func ContextRemainingDef() *ToolDef {
	return &ToolDef{
		Name:        ToolContextRemaining,
		DisplayName: "Context Remaining",
		Category:    "context",
		Description: "Get the remaining tokens in the current context window. Ask before a large read when you are unsure whether it will fit. Returns null when the figure is unavailable (streaming providers do not always report it).",
		InputSchema: map[string]ParamDef{},
		OutputSchema: map[string]ParamDef{
			"tokens_left": {Type: "integer", Description: "Remaining tokens, or null when unavailable"},
			"tokens_used": {Type: "integer", Description: "Tokens currently in the window"},
			"max_tokens":  {Type: "integer", Description: "The window's budget"},
		},
		EstimatedTime: 0,
	}
}

// ContextCompactDef starts a new context window.
func ContextCompactDef() *ToolDef {
	return &ToolDef{
		Name:        ToolContextCompact,
		DisplayName: "Compact Context",
		Category:    "context",
		Description: "Start a new context window, summarising the conversation so far and archiving the original messages so they can still be recalled with context.recall. Does not clear, reset, or otherwise affect environment state: files, side effects and world state are untouched, so never redo work because of a compaction.",
		InputSchema: map[string]ParamDef{},
		OutputSchema: map[string]ParamDef{
			"tokens_used": {Type: "integer", Description: "Tokens in the window after compacting"},
			"summary":     {Type: "string", Description: "The summary that replaced the older turns"},
		},
		EstimatedTime: 0,
	}
}

// ContextRecallDef searches the archived conversation.
func ContextRecallDef() *ToolDef {
	return &ToolDef{
		Name:        ToolContextRecall,
		DisplayName: "Recall Archived Context",
		Category:    "context",
		Description: "Search the conversation that was archived by earlier compactions, and get matching excerpts with the step they came from. Use it when a summary lost a detail you now need. Archived text is a historical snapshot, not current state — re-verify anything that may have changed.",
		InputSchema: map[string]ParamDef{
			"query": {Type: "string", Description: "Text to search for in the archived conversation", Required: true},
			"limit": {Type: "integer", Description: "Maximum excerpts to return (default 5)"},
		},
		OutputSchema: map[string]ParamDef{
			"matches": {Type: "array", Description: "Excerpts with {step, role, excerpt}"},
			"status":  {Type: "string", Description: `"ok", "no_match", "no_archive" or "archive_unavailable"`},
		},
		RequiredParams: []string{"query"},
		EstimatedTime:  0,
	}
}

// contextToolUnavailable is the handler for every context tool. It is not a
// silent no-op: reaching a handler means the loop did not intercept the call,
// and that has to be visible.
func contextToolUnavailable(name string) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf(
			"%s must be handled by the agent loop, not dispatched as a handler; "+
				"reaching this handler means the interception is missing", name)
	}
}
