package planning

import (
	"context"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDAGGeneratorTemplateStrategy(t *testing.T) {
	mock := &mockLLMClient{fallback: "should not be called"}
	registry, err := tools.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &core.VideoRequirement{
		FaceSwap: &core.FaceSwapReq{
			TargetFace: core.MediaRef{URL: "https://cdn.example.com/face.jpg"},
		},
		TTS:       &core.TTSReq{Text: "Hello", Voice: "zh-CN-XiaoxiaoNeural", Language: "zh-CN"},
		BGM:       &core.BGMReq{Style: "upbeat", Volume: 0.3},
		Subtitles: &core.SubtitleReq{Language: "zh-CN"},
		SourceVideos: []core.MediaRef{
			{URL: "https://cdn.example.com/source.mp4"},
		},
		Resolution: "1080p",
	}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "template", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	assert.NotNil(t, result.DAG)
	assert.Equal(t, "face-swap-with-tts", result.DAG.Name)
	assert.True(t, len(result.DAG.Tasks) > 10) // template has 15 tasks
}

func TestDAGGeneratorLLMStrategy(t *testing.T) {
	validDAG := `name: llm-generated
tasks:
  download:
    handler: media.download
    params:
      url: "https://example.com/video.mp4"
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
  upload:
    handler: media.upload
    params:
      file_path: "${trim.output_path}"
    depends_on:
      - trim
    timeout: 120s`

	mock := &mockLLMClient{fallback: validDAG}
	registry, err := tools.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	// No template match — will use LLM.
	req := &core.VideoRequirement{
		Description: "trim a video",
		SourceVideos: []core.MediaRef{
			{URL: "https://example.com/video.mp4"},
		},
	}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	assert.NotNil(t, result.DAG)
	assert.Equal(t, "llm-generated", result.DAG.Name)
}

func TestDAGGeneratorLLMRetry(t *testing.T) {
	callCount := 0
	mock := &countingMockLLM{
		responses: []string{
			// First attempt: invalid — missing handler.
			`name: bad
tasks:
  t1:
    params: {}`,
			// Second attempt: valid.
			`name: retry-success
tasks:
  download:
    handler: media.download
    params:
      url: "test"`,
		},
		callCount: &callCount,
	}

	registry, err := tools.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &core.VideoRequirement{Description: "download a file"}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, 1, result.Retries) // succeeded on second attempt
	assert.Equal(t, "retry-success", result.DAG.Name)
}

func TestDAGGeneratorFallbackStrategy(t *testing.T) {
	// LLM always returns invalid DAG.
	mock := &mockLLMClient{fallback: "this is not yaml"}
	registry, err := tools.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &core.VideoRequirement{
		Description: "something complex",
		SourceVideos: []core.MediaRef{
			{URL: "https://example.com/source.mp4"},
		},
		Resolution: "720p",
	}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "fallback", result.Strategy)
	assert.NotNil(t, result.DAG)
	assert.Equal(t, "fallback-pipeline", result.DAG.Name)
	assert.Len(t, result.DAG.Tasks, 3) // download, encode, upload
}

func TestDAGGeneratorFallbackUsesReqParams(t *testing.T) {
	mock := &mockLLMClient{fallback: "invalid"}
	registry, err := tools.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &core.VideoRequirement{
		SourceVideos: []core.MediaRef{
			{URL: "https://cdn.example.com/my-video.mp4"},
		},
		Resolution: "4K",
	}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "fallback", result.Strategy)
	assert.Contains(t, result.YAML, "https://cdn.example.com/my-video.mp4")
	assert.Contains(t, result.YAML, "4K")
}

// countingMockLLM returns sequential responses and tracks call count.
type countingMockLLM struct {
	responses []string
	callCount *int
}

func (m *countingMockLLM) Chat(_ context.Context, _ []core.Message) (string, error) {
	idx := *m.callCount
	*m.callCount++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return m.responses[len(m.responses)-1], nil
}
