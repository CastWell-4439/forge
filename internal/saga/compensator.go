// Package saga implements the Saga compensation pattern for distributed transactions.
// When a workflow task fails with on_failure: COMPENSATE, the Saga compensator
// executes completed tasks' compensate handlers in reverse topological order.
package saga

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/castwell/forge/internal/storage"
)

// DAGView is the minimal interface saga needs from a DAG.
// coordinator.DAG satisfies this interface.
type DAGView interface {
	TopologicalSort() ([]string, error)
	TaskCompensateHandler(taskName string) string // returns "" if no compensate handler
}

// CompensationResult records the outcome of a single compensation step.
type CompensationResult struct {
	TaskName   string
	Handler    string
	Success    bool
	ErrorMsg   string
}

// CompensationPlan describes what needs to be compensated and in what order.
type CompensationPlan struct {
	WorkflowID     string
	FailedTask     string
	Steps          []CompensationStep // in reverse topological order
}

// CompensationStep is a single step in a compensation plan.
type CompensationStep struct {
	TaskName          string
	CompensateHandler string
	OriginalTaskID    string
	OriginalInput     json.RawMessage
}

// Compensator orchestrates Saga compensation for failed workflows.
type Compensator struct {
	store storage.Storage
}

// NewCompensator creates a new Saga compensator.
func NewCompensator(store storage.Storage) *Compensator {
	return &Compensator{store: store}
}

// BuildPlan builds a compensation plan for a failed workflow.
// It determines which completed tasks need compensation and in what order.
func (c *Compensator) BuildPlan(ctx context.Context, dag DAGView, workflowID, failedTaskName string) (*CompensationPlan, error) {
	// Get topological order.
	topoOrder, err := dag.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	// Get all tasks for this workflow.
	tasks, err := c.store.ListTasksByWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	// Build lookup: task_name -> storage.Task
	taskByName := make(map[string]*storage.Task)
	for _, t := range tasks {
		taskByName[t.TaskName] = t
	}

	// Reverse topological order.
	reversed := make([]string, len(topoOrder))
	for i, name := range topoOrder {
		reversed[len(topoOrder)-1-i] = name
	}

	// Build compensation steps for completed tasks with compensate handlers.
	plan := &CompensationPlan{
		WorkflowID: workflowID,
		FailedTask: failedTaskName,
	}

	for _, taskName := range reversed {
		task, exists := taskByName[taskName]
		if !exists {
			continue
		}
		// Only compensate completed tasks.
		if task.Status != storage.TaskStatusCompleted {
			continue
		}
		// Only if the DAG defines a compensate handler.
		compHandler := dag.TaskCompensateHandler(taskName)
		if compHandler == "" {
			continue
		}

		plan.Steps = append(plan.Steps, CompensationStep{
			TaskName:          taskName,
			CompensateHandler: compHandler,
			OriginalTaskID:    task.ID,
			OriginalInput:     task.Input,
		})
	}

	return plan, nil
}

// Execute runs a compensation plan. It executes each step sequentially
// (in reverse topological order) and collects results.
// executeFn is the function that actually calls the worker — injected for testability.
func Execute(plan *CompensationPlan, executeFn func(step CompensationStep) error) []CompensationResult {
	results := make([]CompensationResult, 0, len(plan.Steps))

	for _, step := range plan.Steps {
		log.Printf("INFO: saga: compensating %q with %q", step.TaskName, step.CompensateHandler)

		err := executeFn(step)
		result := CompensationResult{
			TaskName: step.TaskName,
			Handler:  step.CompensateHandler,
			Success:  err == nil,
		}
		if err != nil {
			result.ErrorMsg = err.Error()
			log.Printf("WARN: saga: compensation for %q failed: %v", step.TaskName, err)
		}

		results = append(results, result)
	}

	return results
}

// AllSucceeded returns true if all compensation steps succeeded.
func AllSucceeded(results []CompensationResult) bool {
	for _, r := range results {
		if !r.Success {
			return false
		}
	}
	return true
}
