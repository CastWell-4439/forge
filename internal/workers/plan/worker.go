// Package planworker exposes the plan-and-execute half of the agent design as a
// workflow worker.
//
// The design has two halves: plan a DAG and run it, then reflect on the result.
// Both were implemented; only the ReAct half was reachable. This is the entry
// point that makes the other one usable — a workflow can declare `handler:
// planner` and hand it a requirement, and it will produce a plan, submit it as a
// child workflow, and report what happened.
package planworker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/planning"
)

// Worker is the plan-and-execute workflow worker.
//
// It holds a Runner rather than the pieces, because the pieces are the runner's
// business: this type's only job is the workflow worker contract — read params,
// run, return JSON.
type Worker struct {
	runner *planning.Runner
}

// NewWorker creates the worker. A nil runner is refused by Execute rather than
// panicking, so a misconfigured deployment gets an error naming the problem.
func NewWorker(runner *planning.Runner) *Worker {
	return &Worker{runner: runner}
}

// Execute runs one plan-and-execute cycle.
//
// `action` must be "run". Params:
//
//	task        the requirement in prose (required) — same key the agent worker
//	            uses, so a workflow author does not have to learn a second one
//	acceptance  what "done" means (optional); criteria and checks
//	timeout     how long to wait for the child run, as a duration string
//	            (optional; the workflow task's own timeout does not bound this
//	            because the wait happens inside one task)
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	if action != "run" {
		return "", fmt.Errorf("planner worker: unknown action %q (supported: run)", action)
	}
	if w.runner == nil {
		return "", fmt.Errorf("planner worker: no runner configured")
	}

	task, _ := params["task"].(string)
	if task == "" {
		// "prompt" is accepted as an alias for the same reason the agent worker
		// accepts it: authors reach for whichever word they think of first, and
		// rejecting one of them teaches nothing.
		task, _ = params["prompt"].(string)
	}
	// Whitespace is not a requirement. It would pass a presence check and produce
	// a plan whose steps say nothing, which is worse than an error — so the trim
	// happens here, and what is stored is the trimmed text.
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("planner worker: 'task' parameter is required (state the requirement in prose)")
	}

	req := &planning.Requirement{
		Description: task,
		Acceptance:  acceptanceFrom(params),
	}

	// The requirement's own context fields travel as declared, if any.
	if fields, ok := params["fields"].(map[string]any); ok {
		req.Fields = fields
	}

	result, err := w.runner.PlanAndRun(ctx, req, planning.RunOptions{
		Timeout: waitTimeout(params),
	})
	if err != nil {
		return "", fmt.Errorf("planner worker: %w", err)
	}

	out := map[string]any{
		"workflow_id": result.WorkflowID,
		"strategy":    result.Strategy,
		"plan":        result.YAML,
		"timed_out":   result.TimedOut,
	}
	if result.Snapshot != nil {
		out["status"] = result.Snapshot.Status
		out["error"] = result.Snapshot.ErrorMsg
		out["tasks"] = taskOutcomes(result.Snapshot)
		out["acceptance"] = planning.CheckAcceptance(req.Acceptance, result.Snapshot)
	}

	body, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("planner worker: marshal result: %w", err)
	}
	return string(body), nil
}

// waitTimeout reads how long to wait for the child run.
//
// It is a param rather than the task's own timeout because the wait happens
// inside a single task: the coordinator sees one task running, and its deadline
// would kill the wait rather than the child. A caller that wants a bound states
// it here; without one the wait is unbounded, which the child's own task
// deadlines usually make finite anyway.
func waitTimeout(params map[string]any) time.Duration {
	switch v := params["timeout"].(type) {
	case string:
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	case float64:
		// A YAML integer arrives as a number; read it as seconds, which is the
		// unit every other timeout in this system uses.
		if v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 0
}

// acceptanceFrom reads the declared acceptance.
//
// Both shapes are accepted: `acceptance` as a string (the common case — one
// sentence saying what done means) and as an object with criteria and checks
// (when the author wants them ticked off individually). A workflow author who
// wrote one sentence should not have to wrap it in a structure to be understood.
func acceptanceFrom(params map[string]any) planning.Acceptance {
	switch v := params["acceptance"].(type) {
	case string:
		return planning.Acceptance{Criteria: v}
	case map[string]any:
		acc := planning.Acceptance{}
		acc.Criteria, _ = v["criteria"].(string)
		if raw, ok := v["checks"].([]any); ok {
			for _, item := range raw {
				if s, ok := item.(string); ok && s != "" {
					acc.Checks = append(acc.Checks, s)
				}
			}
		}
		return acc
	default:
		return planning.Acceptance{}
	}
}

// taskOutcomes renders each task's status and output for the caller.
//
// Outputs are included because they are the evidence: the acceptance match is
// made against them, so a caller that sees an unmet check needs them in hand to
// judge for itself.
func taskOutcomes(snap *planning.WorkflowSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(snap.Tasks))
	for _, t := range snap.Tasks {
		entry := map[string]any{
			"name":   t.Name,
			"status": t.Status,
		}
		if len(t.Output) > 0 {
			var decoded any
			if err := json.Unmarshal(t.Output, &decoded); err == nil {
				entry["output"] = decoded
			} else {
				// Not JSON: hand back the text rather than dropping it. A worker
				// that returned prose still said something.
				entry["output"] = string(t.Output)
			}
		}
		if t.ErrorMsg != "" {
			entry["error"] = t.ErrorMsg
		}
		out = append(out, entry)
	}
	return out
}
