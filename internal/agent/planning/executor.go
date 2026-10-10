package planning

import (
	"fmt"
	"strings"
)

// The executor handler.
//
// A generated DAG dispatches every step to `agent`, and says what to achieve
// rather than which tool to use:
//
//	handler: agent
//	params:
//	  task:       裁剪视频到 5~15 秒
//	  acceptance: 输出时长在 5~15 秒之间，且内容与原文一致
//
// Tool selection belongs at execution time, not at planning time. The step's
// executor has the workspace, the actual data and the tool registry in front of
// it; the planner has a sentence. Asking the planner to name a tool makes it
// guess about a situation it cannot see — and it is how the previous design
// produced DAGs naming agent tools (`web.fetch`) as workflow handlers, which no
// worker could dispatch.
//
// The trade this makes: a step that could have been a fixed, deterministic
// handler now runs through a model. That is the right default for generated
// plans, which exist precisely because the steps were not known in advance. A
// workflow whose steps ARE known should be written by hand and use those
// handlers directly — which is what the declarative workflow format is for.
const (
	// agentHandler is the handler every generated step dispatches to.
	agentHandler = "agent"
	// agentAction is the action that runs a task to completion.
	agentAction = "run"
	// paramTask carries what the step must achieve.
	paramTask = "task"
	// paramAcceptance carries what "done" means for that step.
	paramAcceptance = "acceptance"
)

// AgentHandlerSpec is the catalog entry for the executor, for assembly to build
// alongside the workers it registered.
//
// Required names the params a generated step must carry. `action` is required by
// the worker adapter rather than by the model, so it is listed here and filled in
// by the generator — the model is never asked to produce it, and L4 sees a task
// that has it.
func AgentHandlerSpec() HandlerSpec {
	return HandlerSpec{
		Name: agentHandler,
		Description: "执行一个自包含的任务并返回结果。参数 task 说明要达成什么，" +
			"acceptance 说明什么算做完；具体用什么工具由执行者当场决定",
		Required: []string{"action", paramTask},
	}
}

// agentParams builds the params for one generated step.
//
// `action` is written here rather than requested from the model: it is fixed
// (`run`) and a model asked to produce it would sometimes produce something else,
// which L4 would then reject for no good reason.
func agentParams(task string, acceptance string) map[string]interface{} {
	params := map[string]interface{}{
		"action":  agentAction,
		paramTask: task,
	}
	if acceptance != "" {
		params[paramAcceptance] = acceptance
	}
	return params
}

// acceptanceText renders an Acceptance for a step's params.
//
// Criteria and checks both go in, because they answer different questions: the
// criterion says what outcome is wanted, the checks say how anyone will know.
// Dropping the checks would leave the executor aiming at prose alone.
func acceptanceText(a Acceptance) string {
	var parts []string
	if a.Criteria != "" {
		parts = append(parts, a.Criteria)
	}
	if len(a.Checks) > 0 {
		parts = append(parts, "检查项: "+strings.Join(a.Checks, "; "))
	}
	return strings.Join(parts, "\n")
}

// validateAgentParams is L4 for an executor step: it must say what to do, and if
// it declares an acceptance it must not be blank.
//
// The acceptance is optional because a step whose outcome is obvious from its
// task does not need one repeated; a step that declares an empty one is a
// different problem — it looks specified and is not, so it is rejected rather
// than silently treated as absent.
func validateAgentParams(taskName string, params map[string]interface{}) []ValidationIssue {
	var issues []ValidationIssue

	raw, ok := params[paramTask]
	if !ok {
		issues = append(issues, ValidationIssue{
			Level:    "L4",
			Severity: SeverityError,
			Message:  fmt.Sprintf("task %q: agent step is missing param %q (say what the step must achieve)", taskName, paramTask),
		})
		return issues
	}
	if s, _ := raw.(string); strings.TrimSpace(s) == "" {
		issues = append(issues, ValidationIssue{
			Level:    "L4",
			Severity: SeverityError,
			Message:  fmt.Sprintf("task %q: param %q is empty (a step that says nothing cannot be executed)", taskName, paramTask),
		})
	}

	if rawAcceptance, ok := params[paramAcceptance]; ok {
		if s, _ := rawAcceptance.(string); strings.TrimSpace(s) == "" {
			issues = append(issues, ValidationIssue{
				Level:    "L4",
				Severity: SeverityError,
				Message: fmt.Sprintf("task %q: param %q is present but empty; either state the criterion or omit it",
					taskName, paramAcceptance),
			})
		}
	}

	return issues
}
