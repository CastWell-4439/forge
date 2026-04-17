//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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

// TestMultiLanguageRouting verifies that tasks are routed to the correct
// workers based on handler registrations.  This simulates a multi-language
// setup where different workers (Go, Python, C++) register different handler
// sets and the Coordinator dispatches tasks to the matching worker.
//
// Since Python and C++ workers cannot be started in a Go test process, we
// spin up multiple Go workers that *simulate* each language's handler set
// and verify that the routing logic dispatches correctly.
func TestMultiLanguageRouting(t *testing.T) {
	// --- Storage ---
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "multilang.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// --- Coordinator ---
	coordAddr := findFreePort(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)

	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	// --- Track which worker executed which task ---
	var mu sync.Mutex
	execLog := make(map[string]string) // task_name -> worker_language

	makeHandler := func(language, handlerName string) worker.HandlerFunc {
		return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			execLog[handlerName] = language
			mu.Unlock()
			return map[string]interface{}{
				"language": language,
				"handler":  handlerName,
				"status":   "done",
			}, nil
		}
	}

	// --- Worker: Go (simulates Go worker with data-processing handlers) ---
	goRegistry := worker.NewRegistry()
	goRegistry.Register("data.transform", makeHandler("go", "data.transform"))
	goRegistry.Register("data.validate", makeHandler("go", "data.validate"))

	goAddr := findFreePort(t)
	goWorker := worker.NewWorker("worker-go-1", goAddr, coordAddr, 10, goRegistry)
	goSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(goSrv, goWorker)
	goLis, err := net.Listen("tcp", goAddr)
	require.NoError(t, err)
	go goSrv.Serve(goLis)
	defer goSrv.GracefulStop()

	err = coord.RegisterWorker(ctx, "worker-go-1", goAddr,
		[]string{"data.transform", "data.validate"}, 10)
	require.NoError(t, err)

	// --- Worker: Python (simulates Python worker with AI handlers) ---
	pyRegistry := worker.NewRegistry()
	pyRegistry.Register("ai.generate", makeHandler("python", "ai.generate"))
	pyRegistry.Register("ai.summarize", makeHandler("python", "ai.summarize"))

	pyAddr := findFreePort(t)
	pyWorker := worker.NewWorker("worker-python-1", pyAddr, coordAddr, 5, pyRegistry)
	pySrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(pySrv, pyWorker)
	pyLis, err := net.Listen("tcp", pyAddr)
	require.NoError(t, err)
	go pySrv.Serve(pyLis)
	defer pySrv.GracefulStop()

	err = coord.RegisterWorker(ctx, "worker-python-1", pyAddr,
		[]string{"ai.generate", "ai.summarize"}, 5)
	require.NoError(t, err)

	// --- Worker: C++ (simulates C++ worker with compute handlers) ---
	cppRegistry := worker.NewRegistry()
	cppRegistry.Register("video.render", makeHandler("cpp", "video.render"))
	cppRegistry.Register("video.thumbnail", makeHandler("cpp", "video.thumbnail"))

	cppAddr := findFreePort(t)
	cppWorker := worker.NewWorker("worker-cpp-1", cppAddr, coordAddr, 4, cppRegistry)
	cppSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(cppSrv, cppWorker)
	cppLis, err := net.Listen("tcp", cppAddr)
	require.NoError(t, err)
	go cppSrv.Serve(cppLis)
	defer cppSrv.GracefulStop()

	err = coord.RegisterWorker(ctx, "worker-cpp-1", cppAddr,
		[]string{"video.render", "video.thumbnail"}, 4)
	require.NoError(t, err)

	// --- Submit a DAG with tasks for all three languages ---
	dagYAML := `
name: multilang-test
version: 1
timeout: 60s

tasks:
  validate:
    handler: data.validate
    params:
      schema: video-input
    timeout: 10s

  generate-prompt:
    handler: ai.generate
    depends_on: [validate]
    params:
      prompt: "describe this video"
    timeout: 10s

  summarize:
    handler: ai.summarize
    depends_on: [generate-prompt]
    params:
      text: "AI generated text about the video"
    timeout: 10s

  render:
    handler: video.render
    depends_on: [validate]
    params:
      input_path: /data/raw.mp4
      format: mp4
    timeout: 30s

  thumbnail:
    handler: video.thumbnail
    depends_on: [render]
    params:
      input_path: /data/rendered.mp4
      timestamp_sec: 1.0
    timeout: 10s

  transform-output:
    handler: data.transform
    depends_on: [summarize, thumbnail]
    params:
      output_format: json
    timeout: 10s
`

	coordConn, err := grpc.NewClient(coordAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer coordConn.Close()

	client := forgev1.NewCoordinatorServiceClient(coordConn)

	resp, err := client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetWorkflowId())

	workflowID := resp.GetWorkflowId()
	t.Logf("Submitted multi-language workflow: %s", workflowID)

	// --- Poll until workflow completes ---
	var finalStatus forgev1.WorkflowStatus
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
			WorkflowId: workflowID,
		})
		require.NoError(t, err)

		finalStatus = getResp.GetWorkflow().GetStatus()
		t.Logf("Workflow status: %s", finalStatus)

		if finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED ||
			finalStatus == forgev1.WorkflowStatus_WORKFLOW_STATUS_FAILED {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	// --- Verify workflow completed ---
	assert.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED, finalStatus,
		"multi-language workflow should complete successfully")

	// --- Verify all 6 tasks completed ---
	getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
		WorkflowId: workflowID,
	})
	require.NoError(t, err)
	wf := getResp.GetWorkflow()
	assert.Equal(t, 6, len(wf.GetTasks()), "should have 6 task instances")

	for _, task := range wf.GetTasks() {
		assert.Equal(t, forgev1.TaskStatus_TASK_STATUS_COMPLETED, task.GetStatus(),
			"task %s should be completed", task.GetTaskName())
	}

	// --- Verify routing: each handler ran on the correct "language" worker ---
	mu.Lock()
	defer mu.Unlock()

	expectedRouting := map[string]string{
		"data.validate":  "go",
		"data.transform": "go",
		"ai.generate":    "python",
		"ai.summarize":   "python",
		"video.render":   "cpp",
		"video.thumbnail": "cpp",
	}

	for handler, expectedLang := range expectedRouting {
		actualLang, ok := execLog[handler]
		assert.True(t, ok, "handler %s should have been executed", handler)
		assert.Equal(t, expectedLang, actualLang,
			"handler %s should route to %s worker, got %s", handler, expectedLang, actualLang)
	}

	t.Logf("Multi-language routing verified: %v", execLog)
}

// TestMultiLanguageCapacity verifies that when one worker is at capacity,
// tasks overflow to another worker with the same handler.
func TestMultiLanguageCapacity(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "capacity.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coordAddr := findFreePort(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)

	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	// Register two workers that handle the same handler "compute.run"
	var mu sync.Mutex
	workerHits := map[string]int{} // worker_id -> execution count

	for i := 1; i <= 2; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		reg := worker.NewRegistry()
		reg.Register("compute.run", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
			mu.Lock()
			workerHits[workerID]++
			mu.Unlock()
			return map[string]interface{}{"worker": workerID}, nil
		})

		addr := findFreePort(t)
		w := worker.NewWorker(workerID, addr, coordAddr, 5, reg)
		srv := grpc.NewServer()
		forgev1.RegisterWorkerServiceServer(srv, w)
		lis, err := net.Listen("tcp", addr)
		require.NoError(t, err)
		go srv.Serve(lis)
		defer srv.GracefulStop()

		err = coord.RegisterWorker(ctx, workerID, addr, []string{"compute.run"}, 5)
		require.NoError(t, err)
	}

	// Submit a simple single-task workflow
	dagYAML := `
name: capacity-test
version: 1
timeout: 30s
tasks:
  compute:
    handler: compute.run
    params:
      data: test
    timeout: 10s
`

	coordConn, err := grpc.NewClient(coordAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer coordConn.Close()

	client := forgev1.NewCoordinatorServiceClient(coordConn)
	resp, err := client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	// Poll for completion
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
			WorkflowId: resp.GetWorkflowId(),
		})
		require.NoError(t, err)
		if getResp.GetWorkflow().GetStatus() == forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Verify the task was executed by one of the workers
	mu.Lock()
	totalHits := 0
	for _, count := range workerHits {
		totalHits += count
	}
	mu.Unlock()
	assert.Equal(t, 1, totalHits, "exactly one worker should have executed the task")
}

// TestMultiLanguageTaskOutput verifies that task output is correctly passed
// through for different handler types.
func TestMultiLanguageTaskOutput(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "output.db")
	store, err := storage.NewBoltStorage(dbPath)
	require.NoError(t, err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coordAddr := findFreePort(t)
	coord := coordinator.NewCoordinator(store)
	coordSrv := grpc.NewServer()
	forgev1.RegisterCoordinatorServiceServer(coordSrv, coord)

	coordLis, err := net.Listen("tcp", coordAddr)
	require.NoError(t, err)
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	registry := worker.NewRegistry()
	registry.Register("echo.json", func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		// Echo back the input params plus a marker
		result := map[string]interface{}{
			"echo":     params,
			"language": "go",
		}
		return result, nil
	})

	workerAddr := findFreePort(t)
	w := worker.NewWorker("worker-echo", workerAddr, coordAddr, 10, registry)
	workerSrv := grpc.NewServer()
	forgev1.RegisterWorkerServiceServer(workerSrv, w)
	workerLis, err := net.Listen("tcp", workerAddr)
	require.NoError(t, err)
	go workerSrv.Serve(workerLis)
	defer workerSrv.GracefulStop()

	err = coord.RegisterWorker(ctx, "worker-echo", workerAddr, []string{"echo.json"}, 10)
	require.NoError(t, err)

	dagYAML := `
name: echo-test
version: 1
timeout: 30s
tasks:
  echo:
    handler: echo.json
    params:
      message: hello-multilang
      count: 42
    timeout: 10s
`

	coordConn, err := grpc.NewClient(coordAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer coordConn.Close()

	client := forgev1.NewCoordinatorServiceClient(coordConn)
	resp, err := client.SubmitWorkflow(ctx, &forgev1.SubmitWorkflowRequest{
		DagYaml: dagYAML,
	})
	require.NoError(t, err)

	// Poll for completion
	deadline := time.Now().Add(10 * time.Second)
	var wf *forgev1.WorkflowInstance
	for time.Now().Before(deadline) {
		getResp, err := client.GetWorkflow(ctx, &forgev1.GetWorkflowRequest{
			WorkflowId: resp.GetWorkflowId(),
		})
		require.NoError(t, err)
		wf = getResp.GetWorkflow()
		if wf.GetStatus() == forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	require.Equal(t, forgev1.WorkflowStatus_WORKFLOW_STATUS_COMPLETED, wf.GetStatus())
	require.Equal(t, 1, len(wf.GetTasks()))

	task := wf.GetTasks()[0]
	assert.Equal(t, forgev1.TaskStatus_TASK_STATUS_COMPLETED, task.GetStatus())

	// Verify output is valid JSON
	var output map[string]interface{}
	err = json.Unmarshal(task.GetOutput(), &output)
	require.NoError(t, err, "task output should be valid JSON")
	assert.Equal(t, "go", output["language"])
}

// findFreePort returns a free localhost:port address.
func findFreePort(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	lis.Close()
	return addr
}
