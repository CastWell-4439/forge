package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// SubagentName is the delegation tool's name.
const SubagentName = "subagent"

// SubagentDef describes the delegation tool.
//
// The tool is registered only when delegation is enabled (see the serve layer),
// so a deployment that does not want subagents pays nothing for them — no tool
// description in the prompt, no schema, no surface.
//
// It declares Effect write, not read: a child runs with the same tools the
// parent has, so it can change the world. Claiming otherwise would let a
// read-authority run delegate its way around the permission gate, which is the
// exact bypass the effect model exists to prevent.
func SubagentDef(cfg core.SubagentConfig) *ToolDef {
	desc := "Delegate a self-contained task to a child agent and get its result. " +
		"The child works in its own conversation, so a task that needs a lot of exploration " +
		"does not consume this conversation's context. Give it a complete, standalone task: " +
		"the child cannot see this conversation."

	switch cfg.Report {
	case core.SubagentReportAnswer:
		desc += " You receive the child's final answer only."
	case core.SubagentReportSteps:
		desc += " You receive the child's final answer plus its full step trace."
	default:
		desc += " You receive the child's final answer plus a short trace of what it did."
	}
	if cfg.Mode.Continuable() {
		desc += " Pass child_id to give an existing child more work instead of starting a new one."
	}

	schema := map[string]ParamDef{
		"task": {Type: "string", Description: "A complete, standalone task for the child agent", Required: true},
	}
	required := []string{"task"}
	if cfg.Mode.Continuable() {
		schema["child_id"] = ParamDef{
			Type: "string",
			Description: "Continue this existing child instead of starting a new one " +
				"(from a previous call's result)",
		}
	}

	return &ToolDef{
		Name:        SubagentName,
		DisplayName: "Delegate to Subagent",
		Category:    "agent",
		Description: desc,
		InputSchema: schema,
		OutputSchema: map[string]ParamDef{
			"answer":   {Type: "string", Description: "The child agent's final answer"},
			"child_id": {Type: "string", Description: "Child identifier, for continuing later"},
			"reason":   {Type: "string", Description: "Why the child stopped (completed, max_steps, ...)"},
		},
		RequiredParams: required,
		// A child can do anything this agent can, including writing and
		// deleting. Declaring write is the honest floor; the child's own calls
		// are still gated individually by its own authority.
		Effect: EffectWrite,
		// Delegation is not idempotent in general: the child may have written
		// files before it was interrupted. Recovery must not replay it silently.
		Idempotent: false,
	}
}

// SubagentHandler runs delegations through the injected runner.
type SubagentHandler struct {
	runner core.SubagentRunner
	cfg    core.SubagentConfig
}

// NewSubagentHandler wires a handler to a runner.
func NewSubagentHandler(runner core.SubagentRunner, cfg core.SubagentConfig) *SubagentHandler {
	return &SubagentHandler{runner: runner, cfg: cfg}
}

// Handle performs one delegation.
//
// Failure handling follows the same rule as the rest of the tool surface: a
// child that ran and stopped early is reported as an ERROR, not as a successful
// call with a disappointing payload. A model reading a success would assume the
// subtask is done and build on nothing.
func (h *SubagentHandler) Handle(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if h.runner == nil {
		return nil, fmt.Errorf("%s: delegation is not available in this deployment", SubagentName)
	}

	task, _ := params["task"].(string)
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("%s: missing required param 'task'", SubagentName)
	}
	childID, _ := params["child_id"].(string)
	if childID != "" && !h.cfg.Mode.Continuable() {
		return nil, fmt.Errorf(
			"%s: 'child_id' requires continuable mode (this deployment is %q); "+
				"call without it to start a new child", SubagentName, h.cfg.Mode)
	}

	result, err := h.runner.RunSubagent(ctx, core.SubagentRequest{
		Task:    task,
		ChildID: childID,
		Depth:   h.runner.SubagentDepth(ctx),
	})
	if err != nil {
		// The delegation could not be performed at all: the error already says
		// why (depth limit, child failed to start).
		return nil, fmt.Errorf("%s: %w", SubagentName, err)
	}

	payload := map[string]interface{}{
		"answer": result.Answer,
		"reason": result.Reason,
	}
	if result.ChildID != "" {
		payload["child_id"] = result.ChildID
	}
	if len(result.Steps) > 0 {
		payload["steps"] = result.Steps
	}

	// A child that stopped for a human, or that did not finish, is reported as
	// an error. The payload still travels in the message so the parent can see
	// what was produced before the stop — losing partial work would be its own
	// kind of dishonesty.
	if result.Paused {
		return nil, fmt.Errorf("%s: child %s paused and needs a human: %s",
			SubagentName, result.ChildID, result.PauseReason)
	}
	if result.Reason != "" && result.Reason != "completed" {
		return nil, fmt.Errorf("%s: child stopped with reason %q before finishing: %s",
			SubagentName, result.Reason, result.Answer)
	}

	return payload, nil
}

// SubagentUnavailable is the handler registered when delegation is disabled.
//
// It exists so a mis-wired deployment fails loudly. If the tool were simply not
// registered, a model that had it in a cached prompt would get "unknown tool" —
// true but unhelpful. Naming the reason lets an operator fix the configuration.
func SubagentUnavailable() HandlerFunc {
	return func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("%s: delegation is disabled in this deployment "+
			"(set the subagent mode to enable it)", SubagentName)
	}
}

// FormatSubagentResult renders a delegation result for a prompt-path
// observation. Kept here so the tool's own rendering and any caller agree.
func FormatSubagentResult(result *core.SubagentResult, report core.SubagentReport) string {
	if result == nil {
		return "{}"
	}
	payload := map[string]interface{}{
		"answer": result.Answer,
		"reason": result.Reason,
	}
	if result.ChildID != "" {
		payload["child_id"] = result.ChildID
	}
	if report != core.SubagentReportAnswer && len(result.Steps) > 0 {
		payload["steps"] = result.Steps
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result.Answer
	}
	return string(encoded)
}
