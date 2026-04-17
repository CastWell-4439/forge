package test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	agentworkers "github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
	"github.com/castwell/forge/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestAgentToolRegistry verifies that all 18 handlers are registered in mock mode.
func TestAgentToolRegistry(t *testing.T) {
	registry := agentworkers.NewToolRegistry()
	cfg := agentworkers.HandlerConfig{
		Mode:      agentworkers.HandlerModeMock,
		Workspace: "/tmp/forge/test",
	}

	err := agentworkers.RegisterAll(registry, cfg)
	require.NoError(t, err)

	assert.Equal(t, 18, registry.Count(), "should have 18 registered tools")

	// Verify all expected handler names are present
	expectedHandlers := []string{
		"media.download", "media.upload",
		"video.probe", "video.preprocess",
		"ai.face_swap", "ai.multi_face_swap", "ai.lip_sync",
		"ai.tts", "ai.script", "ai.subtitle_gen",
		"video.encode", "video.trim", "video.concat", "video.subtitles",
		"audio.mix", "audio.bgm_select",
		"quality.video_check", "quality.face_check",
	}

	for _, name := range expectedHandlers {
		assert.True(t, registry.HasHandler(name), "handler %q should be registered", name)
		assert.NotNil(t, registry.GetTool(name), "tool def %q should exist", name)
	}
}

// TestAgentMockHandlers invokes each mock handler individually to verify plausible output.
func TestAgentMockHandlers(t *testing.T) {
	registry := agentworkers.NewToolRegistry()
	cfg := agentworkers.HandlerConfig{
		Mode:      agentworkers.HandlerModeMock,
		Workspace: "/tmp/forge/test",
	}
	require.NoError(t, agentworkers.RegisterAll(registry, cfg))

	ctx := context.Background()

	t.Run("media.download", func(t *testing.T) {
		handler := registry.GetHandler("media.download")
		result, err := handler(ctx, map[string]interface{}{
			"url": "https://example.com/video.mp4",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, result["file_path"])
		assert.Greater(t, result["file_size"].(int64), int64(0))
	})

	t.Run("media.upload", func(t *testing.T) {
		handler := registry.GetHandler("media.upload")
		result, err := handler(ctx, map[string]interface{}{
			"file_path": "/tmp/forge/test/output/video.mp4",
		})
		require.NoError(t, err)
		assert.Contains(t, result["url"].(string), "https://")
	})

	t.Run("video.probe", func(t *testing.T) {
		handler := registry.GetHandler("video.probe")
		result, err := handler(ctx, map[string]interface{}{
			"video_path": "/tmp/forge/test/input/video.mp4",
		})
		require.NoError(t, err)
		assert.Equal(t, "h264", result["codec"])
		assert.Equal(t, 1920, result["width"])
		assert.Equal(t, 1080, result["height"])
		assert.Equal(t, true, result["decodable"])
	})

	t.Run("video.preprocess", func(t *testing.T) {
		handler := registry.GetHandler("video.preprocess")
		result, err := handler(ctx, map[string]interface{}{
			"video_path": "/tmp/forge/test/input/video.mp4",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, result["output_path"])
	})

	t.Run("ai.face_swap", func(t *testing.T) {
		handler := registry.GetHandler("ai.face_swap")
		result, err := handler(ctx, map[string]interface{}{
			"video_path":      "/tmp/forge/test/input/video.mp4",
			"face_image_path": "/tmp/forge/test/input/face.jpg",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, result["output_path"])
		assert.Greater(t, result["faces_detected"].(int), 0)
	})

	t.Run("ai.tts", func(t *testing.T) {
		handler := registry.GetHandler("ai.tts")
		result, err := handler(ctx, map[string]interface{}{
			"text":  "Hello world, this is a test.",
			"voice": "en-US-JennyNeural",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, result["audio_path"])
		assert.Greater(t, result["duration"].(float64), 0.0)
	})

	t.Run("ai.script", func(t *testing.T) {
		handler := registry.GetHandler("ai.script")
		result, err := handler(ctx, map[string]interface{}{
			"topic":            "product demo",
			"duration_seconds": float64(30),
		})
		require.NoError(t, err)
		assert.NotEmpty(t, result["script_text"])
		assert.Greater(t, result["word_count"].(int), 0)
	})

	t.Run("quality.video_check", func(t *testing.T) {
		handler := registry.GetHandler("quality.video_check")
		result, err := handler(ctx, map[string]interface{}{
			"video_path": "/tmp/forge/test/output/video.mp4",
		})
		require.NoError(t, err)
		assert.Equal(t, true, result["pass"])
		assert.Greater(t, result["score"].(float64), 0.0)
	})

	t.Run("quality.face_check", func(t *testing.T) {
		handler := registry.GetHandler("quality.face_check")
		result, err := handler(ctx, map[string]interface{}{
			"video_path":      "/tmp/forge/test/output/video.mp4",
			"face_image_path": "/tmp/forge/test/input/face.jpg",
		})
		require.NoError(t, err)
		assert.Equal(t, true, result["pass"])
		assert.Greater(t, result["similarity"].(float64), 0.7)
	})
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

	// Submit a 3-task linear DAG: download -> probe -> preprocess
	dagYAML := `
name: agent-mock-test
version: 1
timeout: 60s

tasks:
  download-video:
    handler: media.download
    params:
      url: "https://example.com/source.mp4"
      output_dir: "/tmp/forge/test/input"
    timeout: 10s

  probe-video:
    handler: video.probe
    depends_on: [download-video]
    params:
      video_path: "/tmp/forge/test/input/source.mp4"
    timeout: 10s

  preprocess-video:
    handler: video.preprocess
    depends_on: [probe-video]
    params:
      video_path: "/tmp/forge/test/input/source.mp4"
      codec: "libx264"
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
