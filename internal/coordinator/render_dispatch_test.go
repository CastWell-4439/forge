package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/registry"
	"github.com/castwell/forge/internal/storage"
)

// recordingWorker stands in for a worker and captures each dispatched input.
type recordingWorker struct {
	forgev1.WorkerServiceClient // embedded nil: only ExecuteTask is called
	mu                          sync.Mutex
	inputs                      []map[string]any
}

func (r *recordingWorker) ExecuteTask(_ context.Context, req *forgev1.TaskRequest, _ ...grpc.CallOption) (*forgev1.TaskResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var decoded map[string]any
	_ = json.Unmarshal(req.GetInput(), &decoded)
	r.inputs = append(r.inputs, decoded)
	return &forgev1.TaskResponse{Success: true, Output: json.RawMessage(`{"value":"from-a"}`)}, nil
}

func (r *recordingWorker) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.inputs))
	copy(out, r.inputs)
	return out
}

// The execution half of the template contract, end to end through the real
// dispatch path: params are frozen at submit with templates intact, and the
// renderer resolves them at dispatch against the workflow input ({{.inputs}})
// and the predecessor's named output ({{.first}}).
func TestDispatchRendersTemplatesAgainstInputsAndOutputs(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)

	// The same adapter shape cmd/coordinator installs (registry engine into
	// the coordinator seam).
	coord.SetParamRenderer(func(params, inputs, outputs map[string]any) (map[string]any, error) {
		ctx := registry.TemplateContext{}
		for k, v := range outputs {
			ctx[k] = v
		}
		ctx["inputs"] = inputs
		return registry.RenderParams(params, ctx)
	})

	worker := &recordingWorker{}
	coord.mu.Lock()
	coord.workers["w-test"] = &WorkerEntry{
		ID:       "w-test",
		Handlers: []string{"shell"},
		Capacity: 10,
		Client:   worker,
	}
	coord.mu.Unlock()

	dagYAML := `
name: render_chain
tasks:
  a:
    handler: shell
    params:
      msg: "{{.inputs.greeting}}"
    output: first
  b:
    handler: shell
    params:
      got: "{{.first}}"
      plain: "x"
    depends_on: [a]
`
	resp, err := coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
		Input:   json.RawMessage(`{"greeting":"hello"}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetWorkflowId())

	require.Eventually(t, func() bool {
		return len(worker.snapshot()) >= 2
	}, 10*time.Second, 50*time.Millisecond, "both tasks must be dispatched in dependency order")

	got := worker.snapshot()
	assert.Equal(t, "hello", got[0]["msg"], "first task resolves {{.inputs.greeting}}")
	assert.Contains(t, got[1]["got"], "from-a", "second task resolves {{.first}} from the predecessor output")
	assert.Equal(t, "x", got[1]["plain"], "non-template params pass through untouched")
}

// Without a renderer the historical behaviour stands: params are sent
// exactly as submitted.
func TestDispatchWithoutRendererSendsRawParams(t *testing.T) {
	store, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	coord := NewCoordinator(store)
	worker := &recordingWorker{}
	coord.mu.Lock()
	coord.workers["w-test"] = &WorkerEntry{
		ID:       "w-test",
		Handlers: []string{"shell"},
		Capacity: 10,
		Client:   worker,
	}
	coord.mu.Unlock()

	_, err = coord.SubmitWorkflow(context.Background(), &forgev1.SubmitWorkflowRequest{
		DagYaml: `
name: raw_params
tasks:
  a:
    handler: shell
    params:
      msg: "{{.inputs.greeting}}"
`,
		Input: json.RawMessage(`{"greeting":"hello"}`),
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(worker.snapshot()) >= 1
	}, 10*time.Second, 50*time.Millisecond)

	got := worker.snapshot()
	assert.Equal(t, "{{.inputs.greeting}}", got[0]["msg"],
		"no renderer means no rendering — templates stay literal")
}
