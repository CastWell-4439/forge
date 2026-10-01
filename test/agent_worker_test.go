package test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	agentworkers "github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
	"github.com/castwell/forge/internal/worker"
)

// TestAgentToolRegistry verifies that all 17 handlers are registered in mock mode.
func TestAgentToolRegistry(t *testing.T) {
	registry := agentworkers.NewToolRegistry()
	cfg := agentworkers.HandlerConfig{
		Mode:      agentworkers.HandlerModeMock,
		Workspace: "/tmp/forge/test",
	}

	err := agentworkers.RegisterAll(registry, cfg)
	require.NoError(t, err)

	assert.Equal(t, 17, registry.Count(), "should have 17 registered tools")

	// Verify all expected handler names are present
	expectedHandlers := []string{
		"file.read", "file.write", "file.list", "file.edit", "file.glob", "file.search",
		"shell.run",
		"git.status", "git.log", "git.diff",
		"web.fetch", "web.search",
		"code.execute",
		"data.query",
		"ask.user",
		"skill.activate",
		"knowledge.search",
	}

	for _, name := range expectedHandlers {
		assert.True(t, registry.HasHandler(name), "handler %q should be registered", name)
		assert.NotNil(t, registry.GetTool(name), "tool def %q should exist", name)
	}
}

// TestAgentMockHandlers invokes every mock handler to verify plausible output.
func TestAgentMockHandlers(t *testing.T) {
	registry := agentworkers.NewToolRegistry()
	cfg := agentworkers.HandlerConfig{
		Mode:      agentworkers.HandlerModeMock,
		Workspace: "/tmp/forge/test",
	}
	require.NoError(t, agentworkers.RegisterAll(registry, cfg))

	ctx := context.Background()

	cases := []struct {
		name   string
		params map[string]interface{}
	}{
		{"file.read", map[string]interface{}{"path": "main.go"}},
		{"file.write", map[string]interface{}{"path": "out.txt", "content": "hello"}},
		{"file.list", map[string]interface{}{"path": "."}},
		{"file.edit", map[string]interface{}{"path": "main.go", "old_string": "a", "new_string": "b"}},
		{"file.glob", map[string]interface{}{"pattern": "*.go"}},
		{"file.search", map[string]interface{}{"pattern": "func"}},
		{"shell.run", map[string]interface{}{"command": "go test ./..."}},
		{"git.status", map[string]interface{}{}},
		{"git.log", map[string]interface{}{"count": 5}},
		{"git.diff", map[string]interface{}{}},
		{"web.fetch", map[string]interface{}{"url": "https://example.com"}},
		{"web.search", map[string]interface{}{"query": "forge"}},
		{"code.execute", map[string]interface{}{"language": "go", "code": "println(1)"}},
		{"data.query", map[string]interface{}{"sql": "SELECT 1"}},
		{"ask.user", map[string]interface{}{"question": "which branch?", "options": []string{"main", "dev"}}},
		{"skill.activate", map[string]interface{}{"id": "bugfix-triage"}},
		{"knowledge.search", map[string]interface{}{"query": "how to release"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := registry.GetHandler(tc.name)
			require.NotNil(t, handler, "handler %q should be registered", tc.name)
			result, err := handler(ctx, tc.params)
			require.NoError(t, err)
			assert.NotEmpty(t, result, "handler %q should return something", tc.name)
		})
	}
}

// TestAgentDAGEndToEnd submits a 3-task DAG (download -> probe -> preprocess)
// using agent mock handlers through Forge coordinator+worker, and verifies completion.
func TestAgentDAGEndToEnd(t *testing.T) {
	// Setup BoltDB storage
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "agent-test.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start coordinator
	coordAddr := findFreeAddrAgent(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)

	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	// Create a Forge worker.Registry and register agent mock handlers via bridge
	forgeRegistry := worker.NewRegistry()
	agentRegistry := agentworkers.NewToolRegistry()
	cfg := agentworkers.HandlerConfig{
		Mode:      agentworkers.HandlerModeMock,
		Workspace: "/tmp/forge/test",
	}
	require.NoError(t, agentworkers.RegisterAll(agentRegistry, cfg))

	// Bridge: register all agent handlers into the Forge worker registry
	for _, name := range agentRegistry.ListHandlerNames() {
		agentHandler := agentRegistry.GetHandler(name)
		forgeRegistry.Register(name, worker.HandlerFunc(agentHandler))
	}

	// Start worker
	workerAddr := findFreeAddrAgent(t)
	w := worker.NewWorker("agent-test-worker", workerAddr, coordAddr, 10, forgeRegistry)
	workerSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(workerSrv, w)

	workerLis, err := net.Listen("tcp", workerAddr)
	require.NoError(t, err)
	go workerSrv.Serve(workerLis)
	defer workerSrv.GracefulStop()

	// Register worker with coordinator
	allHandlers := agentRegistry.ListHandlerNames()
	err = coord.RegisterWorker(ctx, "agent-test-worker", workerAddr, allHandlers, 10)
	require.NoError(t, err)

	// Submit a 3-task linear DAG: fetch -> inspect -> publish
	dagYAML := `
name: agent-mock-test
version: 1
timeout: 60s

tasks:
  fetch-source:
    handler: web.fetch
    params:
      url: "https://example.com/source.txt"
    timeout: 10s

  inspect-source:
    handler: file.read
    depends_on: [fetch-source]
    params:
      path: "source.txt"
    timeout: 10s

  publish-result:
    handler: file.write
    depends_on: [inspect-source]
    params:
      path: output/result.txt
      content: "done"
    timeout: 30s
`

	// Connect to coordinator
	coordConn, err := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer coordConn.Close()

	client := forgev1.NewCoordinatorServiceClient(coordConn)

	// Submit workflow
	resp, err := client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetWorkflowId())

	workflowID := resp.GetWorkflowId()
	t.Logf("Submitted agent workflow: %s", workflowID)

	// Poll until workflow completes or times out
	var finalStatus forgev1.WorkflowStatus
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
			WorkflowId: workflowID,
		})
		require.NoError(t, err)

		finalStatus = getResp.GetWorkflow().GetStatus()
		t.Logf("Workflow status: %s, tasks: %d", finalStatus, len(getResp.GetWorkflow().GetTasks()))

		if finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED ||
			finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	// Verify workflow completed successfully
	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED, finalStatus,
		"workflow should have completed successfully")

	// Verify all 3 tasks completed
	getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{WorkflowId: workflowID})
	require.NoError(t, err)
	wf := getResp.GetWorkflow()
	assert.Equal(t, 3, len(wf.GetTasks()), "should have 3 task instances")

	for _, task := range wf.GetTasks() {
		assert.Equal(t, forgev1.TaskStatus_TASK_STATUS_COMPLETED, task.GetStatus(),
			"task %s should be completed", task.GetTaskName())
		t.Logf("Task %s: status=%s", task.GetTaskName(), task.GetStatus())
	}

	// Verify task outputs are populated (Forge persists output on completion)
	for _, task := range wf.GetTasks() {
		assert.NotEmpty(t, task.GetOutput(), "task %s should have output", task.GetTaskName())
	}

	t.Logf("Agent DAG end-to-end test passed with 3 tasks")
}

// findFreeAddrAgent returns a free localhost:port address for test servers.
func findFreeAddrAgent(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	lis.Close()
	return addr
}
