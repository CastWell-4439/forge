package worker

import (
	"context"
	"encoding/json"
	"fmt"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// Executor executes tasks by looking up handlers in the registry and invoking them.
type Executor struct {
	registry *Registry
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

	// Execute handler
	result, err := handler(ctx, params)
	if err != nil {
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
