package planning

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/domain"
	"github.com/castwell/forge/internal/agent/workers"
)

func TestDAGGeneratorTemplateStrategy(t *testing.T) {
	mock := &mockLLMClient{fallback: "should not be called"}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &domain.VideoRequirement{
		FaceSwap: &domain.FaceSwapReq{
			TargetFace: domain.MediaRef{URL: "https://cdn.example.com/face.jpg"},
		},
		TTS:       &domain.TTSReq{Text: "Hello", Voice: "zh-CN-XiaoxiaoNeural", Language: "zh-CN"},
		BGM:       &domain.BGMReq{Style: "upbeat", Volume: 0.3},
		Subtitles: &domain.SubtitleReq{Language: "zh-CN"},
		SourceVideos: []domain.MediaRef{
			{URL: "https://cdn.example.com/source.mp4"},
		},
		Resolution: "1080p",
	}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "template", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	assert.NotNil(t, result.DAG)
	assert.Equal(t, "source-pipeline", result.DAG.Name)
	assert.Len(t, result.DAG.Tasks, 9) // fetch x2, prepare, transform, narrate, soundtrack, combine, annotate, publish
}

func TestDAGGeneratorLLMStrategy(t *testing.T) {
	validDAG := `name: llm-generated
tasks:
  fetch:
    handler: web.fetch
    params:
      url: "https://example.com/video.mp4"
    timeout: 60s
  edit:
    handler: file.edit
    params:
      path: "sample.txt"
      old_string: "old"
      new_string: "new"
    depends_on:
      - fetch
    timeout: 30s
  publish:
    handler: file.write
    params:
      path: output/result.txt
      content: "${edit.output}"
    depends_on:
      - edit
    timeout: 120s`

	mock := &mockLLMClient{fallback: validDAG}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	// No template match —will use LLM.
	req := &domain.VideoRequirement{
		Description: "trim a video",
		SourceVideos: []domain.MediaRef{
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
			// First attempt: invalid —missing handler.
			`name: bad
tasks:
  t1:
    params: {}`,
			// Second attempt: valid.
			`name: retry-success
tasks:
  fetch:
    handler: web.fetch
    params:
      url: "test"`,
		},
		callCount: &callCount,
	}

	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &domain.VideoRequirement{Description: "download a file"}

	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, 1, result.Retries) // succeeded on second attempt
	assert.Equal(t, "retry-success", result.DAG.Name)
}

func TestDAGGeneratorFallbackStrategy(t *testing.T) {
	// LLM always returns invalid DAG.
	mock := &mockLLMClient{fallback: "this is not yaml"}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &domain.VideoRequirement{
		Description: "something complex",
		SourceVideos: []domain.MediaRef{
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
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	gen := NewDAGGenerator(mock, registry)

	req := &domain.VideoRequirement{
		SourceVideos: []domain.MediaRef{
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

func (m *countingMockLLM) ChatWithUsage(ctx context.Context, msgs []core.Message) (core.ChatResult, error) {
	content, err := m.Chat(ctx, msgs)
	return core.ChatResult{Content: content}, err
}
