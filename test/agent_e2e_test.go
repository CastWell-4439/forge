package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/planning"
	"github.com/castwell/forge/internal/agent/session"
	"github.com/castwell/forge/internal/agent/workers"
)

// mockLLMForE2E returns predefined responses for the full agent pipeline.
type mockLLMForE2E struct {
	parserResponse  string
	plannerResponse string
}

func (m *mockLLMForE2E) Chat(_ context.Context, messages []core.Message) (string, error) {
	for _, msg := range messages {
		if msg.Role == "system" {
			if contains(msg.Content, "需求分析师") {
				return m.parserResponse, nil
			}
			if contains(msg.Content, "DAG") {
				return m.plannerResponse, nil
			}
		}
	}
	return m.parserResponse, nil
}

func (m *mockLLMForE2E) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
	resp, err := m.Chat(ctx, messages)
	return core.ChatResult{Content: resp}, err
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestAgentE2ETemplateFlow tests the full pipeline:
// input text -> RequirementParser -> TaskPlanner -> DAG generated -> validation passes.
func TestAgentE2ETemplateFlow(t *testing.T) {
	parserJSON := `{
		"description": "30秒产品介绍视频，换脸，配BGM和字幕",
		"duration": 30,
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"face_swap": {
			"target_face": {"url": "https://cdn.example.com/face.jpg", "type": "image", "filename": "face.jpg"},
			"all_faces": false,
			"face_index": [0]
		},
		"tts": {
			"text": "这是一段产品介绍",
			"voice": "zh-CN-XiaoxiaoNeural",
			"language": "zh-CN",
			"speed": 1.0
		},
		"bgm": {
			"style": "upbeat",
			"volume": 0.3
		},
		"subtitles": {
			"language": "zh-CN",
			"style": "default",
			"position": "bottom"
		},
		"source_videos": [
			{"url": "https://cdn.example.com/source.mp4", "type": "video", "filename": "source.mp4"}
		],
		"quality_level": "standard"
	}`

	mock := &mockLLMForE2E{parserResponse: parserJSON}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)

	parser := planning.NewRequirementParser(mock)
	req, err := parser.Parse(context.Background(), "帮我做一个30秒的产品介绍视频，用这张人脸，配轻快的BGM和字幕")
	require.NoError(t, err)
	require.NotNil(t, req)

	assert.Equal(t, "16:9", req.AspectRatio)
	assert.Equal(t, "1080p", req.Resolution)
	require.NotNil(t, req.FaceSwap)
	require.NotNil(t, req.TTS)
	require.NotNil(t, req.BGM)
	require.NotNil(t, req.Subtitles)
	require.Len(t, req.SourceVideos, 1)

	gen := planning.NewDAGGenerator(mock, registry)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "template", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	require.NotNil(t, result.DAG)

	dag := result.DAG
	assert.Equal(t, "source-pipeline", dag.Name)

	expectedTasks := []string{
		"fetch-source",
		"fetch-asset",
		"prepare",
		"transform",
		"narrate",
		"soundtrack",
		"combine",
		"annotate",
		"publish",
	}

	assert.Len(t, dag.Tasks, len(expectedTasks))
	for _, taskName := range expectedTasks {
		task, ok := dag.Tasks[taskName]
		assert.True(t, ok, "expected task %q not found", taskName)
		if ok {
			assert.NotEmpty(t, task.Handler, "task %q has empty handler", taskName)
		}
	}

	assert.Equal(t, "web.fetch", dag.Tasks["fetch-source"].Handler)
	assert.Equal(t, "web.fetch", dag.Tasks["fetch-asset"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["prepare"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["transform"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["narrate"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["soundtrack"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["combine"].Handler)
	assert.Equal(t, "code.execute", dag.Tasks["annotate"].Handler)
	assert.Equal(t, "file.write", dag.Tasks["publish"].Handler)

	assert.Empty(t, dag.Tasks["fetch-source"].DependsOn)
	assert.Empty(t, dag.Tasks["fetch-asset"].DependsOn)

	prepDeps := dag.Tasks["prepare"].DependsOn
	assert.Contains(t, prepDeps, "fetch-source")
	assert.Contains(t, prepDeps, "fetch-asset")

	combineDeps := dag.Tasks["combine"].DependsOn
	assert.Contains(t, combineDeps, "narrate")
	assert.Contains(t, combineDeps, "soundtrack")

	assert.Equal(t, []string{"prepare"}, dag.Tasks["transform"].DependsOn)
	assert.Equal(t, []string{"combine"}, dag.Tasks["annotate"].DependsOn)

	publishDeps := dag.Tasks["publish"].DependsOn
	assert.Contains(t, publishDeps, "transform")
	assert.Contains(t, publishDeps, "annotate")

	err = dag.Validate()
	assert.NoError(t, err, "DAG should pass structural validation")

	sorted, err := dag.TopologicalSort()
	require.NoError(t, err)
	assert.Len(t, sorted, 9)
}

// TestAgentE2ELLMFlow tests the pipeline with LLM-generated DAG.
func TestAgentE2ELLMFlow(t *testing.T) {
	parserJSON := `{
		"description": "裁剪视频5秒到15秒",
		"duration": 10,
		"aspect_ratio": "16:9",
		"resolution": "1080p",
		"source_videos": [
			{"url": "https://cdn.example.com/long-video.mp4", "type": "video", "filename": "long-video.mp4"}
		],
		"quality_level": "draft"
	}`

	llmDAG := `name: trim-video
tasks:
  download:
    handler: web.fetch
    params:
      url: "https://cdn.example.com/long-video.mp4"
    timeout: 60s
  trim:
    handler: file.edit
    params:
      path: "long-video.mp4"
      old_string: "old"
      new_string: "new"
    depends_on:
      - download
    timeout: 30s
  encode:
    handler: code.execute
    params:
      language: go
      code: "encode()"
    depends_on:
      - trim
    timeout: 120s
  upload:
    handler: file.write
    params:
      path: output/trimmed.txt
      content: "${encode.stdout}"
    depends_on:
      - encode
    timeout: 120s`

	mock := &mockLLMForE2E{
		parserResponse:  parserJSON,
		plannerResponse: llmDAG,
	}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)

	parser := planning.NewRequirementParser(mock)
	req, err := parser.Parse(context.Background(), "裁剪视频从5秒到15秒")
	require.NoError(t, err)
	assert.Nil(t, req.FaceSwap)
	assert.Nil(t, req.TTS)

	gen := planning.NewDAGGenerator(mock, registry)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	assert.Equal(t, "trim-video", result.DAG.Name)
	assert.Len(t, result.DAG.Tasks, 4)

	assert.Empty(t, result.DAG.Tasks["download"].DependsOn)
	assert.Equal(t, []string{"download"}, result.DAG.Tasks["trim"].DependsOn)
	assert.Equal(t, []string{"trim"}, result.DAG.Tasks["encode"].DependsOn)
	assert.Equal(t, []string{"encode"}, result.DAG.Tasks["upload"].DependsOn)

	err = result.DAG.Validate()
	assert.NoError(t, err)
}

// TestAgentE2ESessionFlow tests the session state machine through a full flow.
func TestAgentE2ESessionFlow(t *testing.T) {
	sess := session.NewSession()
	store := session.NewInMemorySessionStore()

	require.NoError(t, store.Save(sess))

	require.NoError(t, sess.Transition(session.StateParsing))
	sess.AddMessage(core.Message{Role: "user", Content: "make a face swap video"})

	parserJSON := `{
		"description": "face swap video",
		"duration": 30,
		"face_swap": {
			"target_face": {"url": "https://face.jpg", "type": "image"},
			"all_faces": false
		},
		"tts": {"text": "hello", "voice": "en", "language": "en"},
		"bgm": {"style": "chill", "volume": 0.3},
		"subtitles": {"language": "en"},
		"source_videos": [{"url": "https://video.mp4", "type": "video"}],
		"quality_level": "standard"
	}`
	mock := &mockLLMForE2E{parserResponse: parserJSON}
	parser := planning.NewRequirementParser(mock)
	req, err := parser.Parse(context.Background(), "make a face swap video")
	require.NoError(t, err)
	sess.SetRequirement(req)

	require.NoError(t, sess.Transition(session.StatePlanning))

	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := planning.NewDAGGenerator(mock, registry)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "template", result.Strategy)

	require.NoError(t, sess.Transition(session.StateExecuting))
	sess.SetWorkflowID("wf-test-123")

	require.NoError(t, sess.Transition(session.StateChecking))
	require.NoError(t, sess.Transition(session.StateCompleted))

	assert.Equal(t, session.StateCompleted, sess.GetState())
	assert.Equal(t, "wf-test-123", sess.WorkflowID)
	assert.NotNil(t, sess.Requirement)

	retrieved, err := store.Get(sess.ID)
	require.NoError(t, err)
	assert.Equal(t, session.StateCompleted, retrieved.GetState())
}

// TestAgentE2EValidationRoundtrip tests that template-generated DAGs pass
// full 4-layer validation.
func TestAgentE2EValidationRoundtrip(t *testing.T) {
	parserJSON := `{
		"description": "full production video",
		"duration": 60,
		"aspect_ratio": "16:9",
		"resolution": "4K",
		"face_swap": {
			"target_face": {"url": "https://face.png", "type": "image"},
			"all_faces": false
		},
		"tts": {"text": "Product launch narration", "voice": "en-US-Jenny", "language": "en-US", "speed": 1.0},
		"bgm": {"style": "epic", "volume": 0.2},
		"subtitles": {"language": "en-US", "style": "modern", "position": "bottom"},
		"source_videos": [{"url": "https://source.mp4", "type": "video"}],
		"quality_level": "premium"
	}`

	mock := &mockLLMForE2E{parserResponse: parserJSON}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)

	parser := planning.NewRequirementParser(mock)
	req, err := parser.Parse(context.Background(), "Create a premium product launch video")
	require.NoError(t, err)

	gen := planning.NewDAGGenerator(mock, registry)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "template", result.Strategy)

	validator := planning.NewDAGValidator(registry)
	valResult := validator.Validate(result.YAML)
	assert.True(t, valResult.Valid, "validation failed: %v", valResult.Issues)
	assert.False(t, valResult.HasErrors())

	sorted, err := result.DAG.TopologicalSort()
	require.NoError(t, err)
	assert.Len(t, sorted, 9)

	fetchIdx := indexOf(sorted, "fetch-source")
	prepareIdx := indexOf(sorted, "prepare")
	transformIdx := indexOf(sorted, "transform")
	publishIdx := indexOf(sorted, "publish")

	assert.True(t, fetchIdx < prepareIdx, "fetch should precede prepare")
	assert.True(t, prepareIdx < transformIdx, "prepare should precede transform")
	assert.True(t, transformIdx < publishIdx, "transform should precede publish")
}

func indexOf(slice []string, item string) int {
	for i, v := range slice {
		if v == item {
			return i
		}
	}
	return -1
}
