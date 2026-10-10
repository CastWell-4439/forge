package session

import (
	"context"
	"encoding/json"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/agent/planning"
)

// AsSubmitter lets the planner execute the DAGs it produces.
//
// The two halves of the agent design were each complete and unconnected: the
// planning package could build a DAG and the session package could submit one,
// and nothing joined them. This is that join.
//
// It lives in session rather than planning because the dependency has to run one
// way — planning defines what it needs (a Submitter interface) and this is the
// transport's answer. Putting it in planning would make the planner import the
// gRPC client, which is the coupling that kept the halves apart.
func (c *ForgeClient) AsSubmitter() planning.Submitter {
	return &planningSubmitter{client: c}
}

type planningSubmitter struct {
	client *ForgeClient
}

// Submit implements planning.Submitter.
func (p *planningSubmitter) Submit(ctx context.Context, dagYAML string) (string, error) {
	return p.client.Submit(ctx, dagYAML)
}

// Snapshot implements planning.Submitter by translating the coordinator's
// response into the planner's own view of a run.
//
// The translation is the point of the adapter: planning should not have to know
// about proto enums or the shape of a gRPC response, and it should not have to
// change when they do.
func (p *planningSubmitter) Snapshot(ctx context.Context, workflowID string) (*planning.WorkflowSnapshot, error) {
	inst, err := p.client.Get(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	return toSnapshot(inst), nil
}

// toSnapshot converts a workflow instance to the planner's snapshot type.
func toSnapshot(inst *forgev1.WorkflowInstance) *planning.WorkflowSnapshot {
	if inst == nil {
		return &planning.WorkflowSnapshot{Status: "UNKNOWN"}
	}

	snap := &planning.WorkflowSnapshot{
		ID:       inst.GetId(),
		Name:     inst.GetName(),
		Status:   workflowStatusName(inst.GetStatus()),
		ErrorMsg: inst.GetErrorMsg(),
	}
	for _, t := range inst.GetTasks() {
		snap.Tasks = append(snap.Tasks, planning.TaskSnapshot{
			Name: t.GetTaskName(),
			// "handler" is the planner's word for how a step runs; the proto
			// calls it the same thing, so this stays a straight copy.
			Handler:  t.GetHandler(),
			Status:   taskStatusName(t.GetStatus()),
			Output:   json.RawMessage(t.GetOutput()),
			ErrorMsg: t.GetErrorMsg(),
		})
	}
	return snap
}

// workflowStatusName renders a workflow status as the string the planner
// compares against.
//
// It goes through the enum's own name rather than a hand-written switch so a new
// status cannot arrive and be silently mapped to the wrong word. The prefix is
// trimmed because the planner's values are the bare names.
func workflowStatusName(s forgev1.WorkflowStatus) string {
	return trimEnumPrefix(s.String(), "WORKFLOW_STATUS_")
}

// taskStatusName renders a task status the same way.
func taskStatusName(s forgev1.TaskStatus) string {
	return trimEnumPrefix(s.String(), "TASK_STATUS_")
}

// trimEnumPrefix strips a proto enum's common prefix, leaving the bare name.
// A name that does not carry the prefix is returned unchanged, so an unexpected
// value shows up as itself rather than as something empty.
func trimEnumPrefix(name, prefix string) string {
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}

var _ planning.Submitter = (*planningSubmitter)(nil)
