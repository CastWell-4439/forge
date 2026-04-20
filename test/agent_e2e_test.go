package test

import (
	"context"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/planning"
	"github.com/castwell/forge/internal/agent/session"
	"github.com/castwell/forge/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	registry := tools.DefaultRegistry()

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
	assert.Equal(t, "face-swap-with-tts", dag.Name)

	expectedTasks := []string{
		"download-source-video",
		"download-face-image",
		"probe-video",
		"preprocess-video",
		"face-swap",
		"generate-tts",
		"select-bgm",
		"mix-audio",
		"lip-sync",
		"generate-subtitles",
		"add-subtitles",
		"encode-output",
		"check-quality",
		"check-face",
		"upload",
	}

	assert.Len(t, dag.Tasks, len(expectedTasks))
	for _, taskName := range expectedTasks {
		task, ok := dag.Tasks[taskName]
		assert.True(t, ok, "expected task %q not found", taskName)
		if ok {
			assert.NotEmpty(t, task.Handler, "task %q has empty handler", taskName)
		}
	}

	assert.Equal(t, "media.download", dag.Tasks["download-source-video"].Handler)
	assert.Equal(t, "media.download", dag.Tasks["download-face-image"].Handler)
	assert.Equal(t, "video.probe", dag.Tasks["probe-video"].Handler)
	assert.Equal(t, "video.preprocess", dag.Tasks["preprocess-video"].Handler)
	assert.Equal(t, "ai.face_swap", dag.Tasks["face-swap"].Handler)
	assert.Equal(t, "ai.tts", dag.Tasks["generate-tts"].Handler)
	assert.Equal(t, "audio.bgm_select", dag.Tasks["select-bgm"].Handler)
	assert.Equal(t, "audio.mix", dag.Tasks["mix-audio"].Handler)
	assert.Equal(t, "ai.lip_sync", dag.Tasks["lip-sync"].Handler)
	assert.Equal(t, "ai.subtitle_gen", dag.Tasks["generate-subtitles"].Handler)
	assert.Equal(t, "video.subtitles", dag.Tasks["add-subtitles"].Handler)
	assert.Equal(t, "video.encode", dag.Tasks["encode-output"].Handler)
	assert.Equal(t, "quality.video_check", dag.Tasks["check-quality"].Handler)
	assert.Equal(t, "quality.face_check", dag.Tasks["check-face"].Handler)
	assert.Equal(t, "media.upload", dag.Tasks["upload"].Handler)

	assert.Empty(t, dag.Tasks["download-source-video"].DependsOn)
	assert.Empty(t, dag.Tasks["download-face-image"].DependsOn)
	assert.Equal(t, []string{"download-source-video"}, dag.Tasks["probe-video"].DependsOn)

	fsDeps := dag.Tasks["face-swap"].DependsOn
	assert.Contains(t, fsDeps, "preprocess-video")
	assert.Contains(t, fsDeps, "download-face-image")

	mixDeps := dag.Tasks["mix-audio"].DependsOn
	assert.Contains(t, mixDeps, "generate-tts")
	assert.Contains(t, mixDeps, "select-bgm")

	lsDeps := dag.Tasks["lip-sync"].DependsOn
	assert.Contains(t, lsDeps, "face-swap")
	assert.Contains(t, lsDeps, "mix-audio")

	uploadDeps := dag.Tasks["upload"].DependsOn
	assert.Contains(t, uploadDeps, "check-quality")
	assert.Contains(t, uploadDeps, "check-face")

	err = dag.Validate()
	assert.NoError(t, err, "DAG should pass structural validation")

	sorted, err := dag.TopologicalSort()
	require.NoError(t, err)
	assert.Len(t, sorted, 15)
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
    handler: media.download
    params:
      url: "https://cdn.example.com/long-video.mp4"
    timeout: 60s
  trim:
    handler: video.trim
    params:
      video_path: "${download.output_path}"
      start_time: "00:00:05"
      end_time: "00:00:15"
    depends_on:
      - download
    timeout: 30s
  encode:
    handler: video.encode
    params:
      video_path: "${trim.output_path}"
    depends_on:
      - trim
    timeout: 120s
  upload:
    handler: media.upload
    params:
      file_path: "${encode.output_path}"
    depends_on:
      - encode
    timeout: 120s`

	mock := &mockLLMForE2E{
		parserResponse:  parserJSON,
		plannerResponse: llmDAG,
	}
	registry := tools.DefaultRegistry()

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

	registry := tools.DefaultRegistry()
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
	registry := tools.DefaultRegistry()

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
	assert.Len(t, sorted, 15)

	downloadIdx := indexOf(sorted, "download-source-video")
	probeIdx := indexOf(sorted, "probe-video")
	faceSwapIdx := indexOf(sorted, "face-swap")
	uploadIdx := indexOf(sorted, "upload")

	assert.True(t, downloadIdx < probeIdx, "download should precede probe")
	assert.True(t, probeIdx < faceSwapIdx, "probe should precede face-swap")
	assert.True(t, faceSwapIdx < uploadIdx, "face-swap should precede upload")
}

func indexOf(slice []string, item string) int {
	for i, v := range slice {
		if v == item {
			return i
		}
	}
	return -1
}
