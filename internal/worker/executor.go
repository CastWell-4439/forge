package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// Executor executes tasks by looking up handlers in the registry and invoking them.
type Executor struct {
	registry          *Registry
	gate              RuntimeGate
	gateFailurePolicy GateFailurePolicy
}

// NewExecutor creates a new Executor with the given handler registry.
func NewExecutor(registry *Registry) *Executor {
	return &Executor{registry: registry}
}

// Execute runs the handler for the given task request and returns the response.
func (e *Executor) Execute(ctx context.Context, req *forgev1.TaskRequest) *forgev1.TaskResponse {
	handler := e.registry.Get(req.GetHandler())
	if handler == nil {
		return &forgev1.TaskResponse{
			TaskId:   req.GetTaskId(),
			Success:  false,
			ErrorMsg: fmt.Sprintf("unknown handler: %s", req.GetHandler()),
		}
	}

	// Decode input parameters
	var params map[string]interface{}
	if len(req.GetInput()) > 0 {
		if err := json.Unmarshal(req.GetInput(), &params); err != nil {
			return &forgev1.TaskResponse{
				TaskId:   req.GetTaskId(),
				Success:  false,
				ErrorMsg: fmt.Sprintf("unmarshal task input: %v", err),
			}
		}
	}
	if params == nil {
		params = make(map[string]interface{})
	}

	if e.gate != nil {
		decision, err := e.gate.BeforeExecute(ctx, GateRequest{
			TaskID:     req.GetTaskId(),
			WorkflowID: req.GetWorkflowId(),
			TaskName:   req.GetTaskName(),
			Handler:    req.GetHandler(),
			Params:     params,
		})
		switch {
		case err != nil:
			// The gate could not decide. The default is fail-open: a broken policy
			// file or review resolver must not take the runtime down. Deployments
			// that prefer to stop on gate failure opt in with WithGateFailurePolicy.
			if e.gateFailurePolicy == GateFailureClosed {
				return &forgev1.TaskResponse{
					TaskId:   req.GetTaskId(),
					Success:  false,
					ErrorMsg: fmt.Sprintf("runtime gate failed (fail-closed): %v", err),
				}
			}
		case decision.Action == GateActionPause && decision.Enforce:
			// Held for a human: the handler does NOT run, and this is not a
			// failure. Reporting it as one (the previous behaviour) made a
			// paused task indistinguishable from a denied one at the
			// coordinator, so "pause" could only ever behave like "block".
			// The pause travels as its own field; the coordinator parks the
			// task and releases this worker.
			return &forgev1.TaskResponse{
				TaskId:      req.GetTaskId(),
				Success:     false,
				Paused:      true,
				PauseReason: decision.Reason,
				GateId:      decision.ID,
			}
		case !GateActionExecutesHandler(decision.Action) && decision.Enforce:
			action := NormalizeGateAction(decision.Action)
			return &forgev1.TaskResponse{
				TaskId:   req.GetTaskId(),
				Success:  false,
				ErrorMsg: fmt.Sprintf("runtime gate %s: %s", action, decision.Reason),
			}
		}
	}

	// Bound the handler by the deadline the coordinator sent.
	//
	// The field existed on the wire and was never read, so a handler could run
	// past its task deadline while the coordinator's sweeper failed the task
	// underneath it — the work still going, and nothing able to stop it. Applying
	// it here cancels the handler instead, and Go's context propagates that into
	// whatever it was doing.
	//
	// Zero means "no deadline", which is what the coordinator sends for a task
	// that declared none.
	if ms := req.GetTimeoutMs(); ms > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}

	// Execute handler
	result, err := handler(ctx, params)
	if err != nil {
		// A handler that queued a human request is not failing: it is stopped
		// until someone answers. Reporting it as a failure would make "waiting on
		// a person" indistinguishable from "broke", and the coordinator would run
		// the failure path over a task that is merely waiting.
		var awaiting *AwaitingHumanError
		if errors.As(err, &awaiting) || errors.Is(err, ErrAwaitingHuman) {
			reason := err.Error()
			if awaiting != nil && awaiting.Message != "" {
				reason = awaiting.Message
			}
			return &forgev1.TaskResponse{
				TaskId:      req.GetTaskId(),
				Success:     false,
				Paused:      true,
				PauseReason: reason,
			}
		}
		return &forgev1.TaskResponse{
			TaskId:   req.GetTaskId(),
			Success:  false,
			ErrorMsg: err.Error(),
		}
	}

	// Encode output
	output, err := json.Marshal(result)
	if err != nil {
		return &forgev1.TaskResponse{
			TaskId:   req.GetTaskId(),
			Success:  false,
			ErrorMsg: fmt.Sprintf("marshal task output: %v", err),
		}
	}

	return &forgev1.TaskResponse{
		TaskId:  req.GetTaskId(),
		Success: true,
		Output:  output,
	}
}
