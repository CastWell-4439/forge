// Package agent implements the agent workflow worker: a task that runs the
// full ReAct agent — the golden tool set, real mode — as a workflow node.
// It is the production entry point that makes the agent tool surface
// reachable from YAML (worker: agent / action: run), which the audit found
// unreachable: the Agent assembly existed only behind tests.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentcore "github.com/castwell/forge/internal/agent"
)

// Worker runs agent sessions as workflow tasks.
type Worker struct {
	agent *agentcore.Agent
}

// NewWorker creates the worker around a fully assembled Agent (tools, mode,
// workspace and backends are the assembly layer's decisions).
func NewWorker(a *agentcore.Agent) *Worker {
	return &Worker{agent: a}
}

// Execute runs one agent session. action must be "run"; the task prompt
// travels in params["task"] (params["prompt"] accepted as an alias). The
// result is the agent's answer plus its run accounting, JSON-encoded like
// every other workflow worker's output.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	if action != "run" {
		return "", fmt.Errorf("agent worker: unknown action %q (supported: run)", action)
	}
	task, _ := params["task"].(string)
	if task == "" {
		task, _ = params["prompt"].(string)
	}
	if task == "" {
		return "", fmt.Errorf("agent worker: missing required param \"task\"")
	}

	// A fresh session per task: this worker does not wire checkpoint/resume
	// (the harness seams stay available for the HITL round), so each run is
	// its own session.
	sessionID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	result, err := w.agent.Run(ctx, sessionID, task)
	if err != nil {
		return "", fmt.Errorf("agent worker: %w", err)
	}

	out, err := json.Marshal(map[string]any{
		"answer": result.Answer,
		"steps":  len(result.Steps),
		"reason": result.Reason,
	})
	if err != nil {
		return "", fmt.Errorf("agent worker: marshal result: %w", err)
	}
	return string(out), nil
}
