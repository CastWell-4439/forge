package test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
	"github.com/castwell/forge/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestLinearDAGEndToEnd starts a Coordinator and Worker in-process,
// submits a 3-task linear DAG (A->B->C) with simple echo handlers,
// and verifies all tasks complete in order and workflow reaches COMPLETED.
func TestLinearDAGEndToEnd(t *testing.T) {
	// Setup BoltDB storage in temp dir
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start coordinator gRPC server
	coordAddr := findFreeAddr(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)

	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	// Create worker with echo handlers for all 3 tasks
	registry := worker.NewRegistry()
	var executionOrder []string
	var executionMu sync.Mutex

	registry.Register("step.a", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		executionMu.Lock()
		executionOrder = append(executionOrder, "A")
		executionMu.Unlock()
		return map[string]interface{}{"step": "A", "status": "done"}, nil
	})
	registry.Register("step.b", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		executionMu.Lock()
		executionOrder = append(executionOrder, "B")
		executionMu.Unlock()
		return map[string]interface{}{"step": "B", "status": "done"}, nil
	})
	registry.Register("step.c", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		executionMu.Lock()
		executionOrder = append(executionOrder, "C")
		executionMu.Unlock()
		return map[string]interface{}{"step": "C", "status": "done"}, nil
	})

	// Start worker gRPC server
	workerAddr := findFreeAddr(t)
	w := worker.NewWorker("test-worker-1", workerAddr, coordAddr, 10, registry)
	workerSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(workerSrv, w)

	workerLis, err := net.Listen("tcp", workerAddr)
	require.NoError(t, err)
	go workerSrv.Serve(workerLis)
	defer workerSrv.GracefulStop()

	// Register worker with coordinator (in-process)
	err = coord.RegisterWorker(ctx, "test-worker-1", workerAddr, []string{"step.a", "step.b", "step.c"}, 10)
	require.NoError(t, err)

	// Submit a linear DAG: A -> B -> C
	dagYAML := `
name: linear-test
version: 1
timeout: 60s

tasks:
  task-a:
    handler: step.a
    params:
      msg: hello-a
    timeout: 10s

  task-b:
    handler: step.b
    depends_on: [task-a]
    params:
      msg: hello-b
    timeout: 10s

  task-c:
    handler: step.c
    depends_on: [task-b]
    params:
      msg: hello-c
    timeout: 10s
`

	// Connect to coordinator as client
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
	t.Logf("Submitted workflow: %s", workflowID)

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
	}

	// Verify execution order: A before B before C
	assert.Equal(t, []string{"A", "B", "C"}, executionOrder,
		"tasks should execute in dependency order: A -> B -> C")

	// Verify events were recorded
	events, err := store.GetWorkflowHistory(ctx, workflowID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(events), 5, "should have at least 5 events (submit, start, 3 task completions)")

	t.Logf("Test passed: %d events recorded", len(events))
}

// TestWorkflowFailure verifies that a task failure propagates to workflow failure.
func TestWorkflowFailure(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-fail.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	coordAddr := findFreeAddr(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)
	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	registry := worker.NewRegistry()
	registry.Register("step.ok", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	registry.Register("step.fail", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	workerAddr := findFreeAddr(t)
	w := worker.NewWorker("test-worker-fail", workerAddr, coordAddr, 10, registry)
	workerSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(workerSrv, w)
	workerLis, err := net.Listen("tcp", workerAddr)
	require.NoError(t, err)
	go workerSrv.Serve(workerLis)
	defer workerSrv.GracefulStop()

	err = coord.RegisterWorker(ctx, "test-worker-fail", workerAddr, []string{"step.ok", "step.fail"}, 10)
	require.NoError(t, err)

	dagYAML := `
name: fail-test
version: 1
timeout: 30s
tasks:
  ok-task:
    handler: step.ok
    timeout: 5s
  fail-task:
    handler: step.fail
    depends_on: [ok-task]
    timeout: 5s
`

	coordConn, err := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer coordConn.Close()

	client := forgev1.NewCoordinatorServiceClient(coordConn)
	resp, err := client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{DagYaml: dagYAML})
	require.NoError(t, err)

	// Poll until done
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus forgev1.WorkflowStatus
	for time.Now().Before(deadline) {
		getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{WorkflowId: resp.GetWorkflowId()})
		require.NoError(t, err)
		finalStatus = getResp.GetWorkflow().GetStatus()
		if finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED ||
			finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED, finalStatus,
		"workflow should have failed due to task failure")
}

// findFreeAddr returns a free localhost:port address.
func findFreeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	lis.Close()
	return addr
}

// Ensure the test file is not empty so the build system picks it up.
func init() {
	// Suppress "imported and not used" for os in edge cases
	_ = os.TempDir
}
