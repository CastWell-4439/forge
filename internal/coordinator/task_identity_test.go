package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/storage"
)

// capturedWorker records the params blob it was handed, so a test can inspect
// what actually reached the worker.
type capturedWorker struct {
	forgev1.WorkerServiceClient
	ch chan map[string]any
}

func newCapturedWorker() *capturedWorker {
	return &capturedWorker{ch: make(chan map[string]any, 8)}
}

func (w *capturedWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	var params map[string]any
	_ = json.Unmarshal(req.GetInput(), &params)
	w.ch <- params
	return &forgev1.TaskResponse{Success: true, Output: json.RawMessage(`{}`)}, nil
}

// A dispatched task must know its own identity. The HITL worker files a request
// that a reviewer answers against a task id, and the id lives in the coordinator
// — not in the YAML the author wrote — so without this the request can never be
// tied back to the task it is blocking.
func TestDispatchedParamsCarryTaskIdentity(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "ident.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	w := newCapturedWorker()
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: w}
	coord.mu.Unlock()

	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: ident\ntasks:\n  a:\n    handler: shell\n    params:\n      keep: yes\n",
	})
	require.NoError(t, err)

	select {
	case params := <-w.ch:
		assert.Equal(t, "a", params["task_name"], "the task name travels with the dispatch")
		assert.Equal(t, resp.GetWorkflowId(), params["workflow_id"])
		assert.NotEmpty(t, params["task_id"], "the task id is what a human request is filed against")
		assert.Equal(t, "yes", params["keep"], "the author's own params are untouched")
	case <-time.After(10 * time.Second):
		t.Fatal("task was never dispatched")
	}
}

// A task with no params at all still gets its identity. The empty case is where
// this broke first: an empty blob decodes to a nil map, and assigning into it
// panicked rather than producing params with just the identity.
func TestDispatchedParamsIdentityWorksWithoutDeclaredParams(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "ident2.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	w := newCapturedWorker()
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: w}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: ident_empty\ntasks:\n  a:\n    handler: shell\n",
	})
	require.NoError(t, err)

	select {
	case params := <-w.ch:
		assert.NotEmpty(t, params["task_id"])
		assert.Equal(t, "a", params["task_name"])
	case <-time.After(10 * time.Second):
		t.Fatal("task was never dispatched")
	}
}

// The identity is written after rendering, so a workflow cannot shadow it: a
// task that declares its own `task_id` must not overwrite the real one, or a
// handler would file its human request against a task that does not exist.
func TestWorkflowCannotShadowTaskIdentity(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "ident3.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	w := newCapturedWorker()
	coord.mu.Lock()
	coord.workers["w-shell"] = &WorkerEntry{ID: "w-shell", Handlers: []string{"shell"}, Capacity: 10, Client: w}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: "name: ident_shadow\ntasks:\n  a:\n    handler: shell\n    params:\n      task_id: \"pretend\"\n",
	})
	require.NoError(t, err)

	select {
	case params := <-w.ch:
		assert.NotEqual(t, "pretend", params["task_id"],
			"the real task id must win over a declared one")
		assert.NotEmpty(t, params["task_id"])
	case <-time.After(10 * time.Second):
		t.Fatal("task was never dispatched")
	}
}

func TestWithTaskIdentityHandlesNullInput(t *testing.T) {
	task := &storage.Task{ID: "t-1", WorkflowID: "wf-1", TaskName: "a"}

	// A literal JSON null is what an empty params blob marshals to.
	out, err := withTaskIdentity(task, json.RawMessage("null"))
	require.NoError(t, err)

	var params map[string]any
	require.NoError(t, json.Unmarshal(out, &params))
	assert.Equal(t, "t-1", params["task_id"])
	assert.Equal(t, "wf-1", params["workflow_id"])
	assert.Equal(t, "a", params["task_name"])
}
