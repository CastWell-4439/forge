package planning

import (
	"context"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPlannerTemplateMatch(t *testing.T) {
	mock := &mockLLMClient{fallback: "should not be called"}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	planner := NewTaskPlanner(mock, registry)

	req := &core.VideoRequirement{
		Description: "face swap video with TTS",
		FaceSwap: &core.FaceSwapReq{
			TargetFace: core.MediaRef{URL: "https://cdn.example.com/face.jpg", Type: "image"},
		},
		TTS:       &core.TTSReq{Text: "Hello world", Voice: "zh-CN-XiaoxiaoNeural", Language: "zh-CN"},
		BGM:       &core.BGMReq{Style: "upbeat", Volume: 0.3},
		Subtitles: &core.SubtitleReq{Language: "zh-CN"},
		SourceVideos: []core.MediaRef{
			{URL: "https://cdn.example.com/source.mp4", Type: "video"},
		},
		Resolution: "1080p",
	}

	dagYAML, err := planner.Plan(context.Background(), req)
	require.NoError(t, err)

	assert.Contains(t, dagYAML, "name: face-swap-with-tts")
	assert.Contains(t, dagYAML, "handler: ai.face_swap")
	assert.Contains(t, dagYAML, "handler: ai.tts")
	assert.Contains(t, dagYAML, "handler: audio.bgm_select")
	assert.Contains(t, dagYAML, "handler: ai.lip_sync")
	assert.Contains(t, dagYAML, "handler: video.subtitles")
	assert.Contains(t, dagYAML, "handler: media.upload")
	assert.Contains(t, dagYAML, "handler: quality.video_check")
	assert.Contains(t, dagYAML, "handler: quality.face_check")

	assert.Contains(t, dagYAML, "https://cdn.example.com/source.mp4")
	assert.Contains(t, dagYAML, "https://cdn.example.com/face.jpg")
	assert.Contains(t, dagYAML, "Hello world")
}

func TestTaskPlannerLLMFallback(t *testing.T) {
	llmDAG := `name: simple-trim
tasks:
  download:
    handler: media.download
    params:
      url: "https://example.com/video.mp4"
  trim:
    handler: video.trim
    params:
      start: "00:00:05"
      end: "00:00:15"
    depends_on:
      - download
  upload:
    handler: media.upload
    params: {}
    depends_on:
      - trim`

	mock := &mockLLMClient{fallback: llmDAG}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	planner := NewTaskPlanner(mock, registry)

	req := &core.VideoRequirement{
		Description: "trim a video from 5s to 15s",
		SourceVideos: []core.MediaRef{
			{URL: "https://example.com/video.mp4", Type: "video"},
		},
	}

	dagYAML, err := planner.Plan(context.Background(), req)
	require.NoError(t, err)
	assert.Contains(t, dagYAML, "name: simple-trim")
	assert.Contains(t, dagYAML, "handler: video.trim")
}

func TestTaskPlannerLLMFallbackWithMarkdown(t *testing.T) {
	llmDAG := "```yaml\nname: test-dag\ntasks:\n  t1:\n    handler: media.download\n    params:\n      url: test\n```"

	mock := &mockLLMClient{fallback: llmDAG}
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	planner := NewTaskPlanner(mock, registry)

	req := &core.VideoRequirement{Description: "just download"}

	dagYAML, err := planner.Plan(context.Background(), req)
	require.NoError(t, err)
	assert.Contains(t, dagYAML, "name: test-dag")
	assert.NotContains(t, dagYAML, "```")
}

func TestSelectTools(t *testing.T) {
	registry, err := workers.DefaultRegistry()
	require.NoError(t, err)
	planner := NewTaskPlanner(nil, registry)

	tests := []struct {
		name     string
		req      *core.VideoRequirement
		expected []string
		excluded []string
	}{
		{
			name: "face swap with TTS",
			req: &core.VideoRequirement{
				FaceSwap:     &core.FaceSwapReq{},
				TTS:          &core.TTSReq{},
				BGM:          &core.BGMReq{},
				Subtitles:    &core.SubtitleReq{},
				QualityLevel: core.QualityStandard,
				SourceVideos: []core.MediaRef{{URL: "test"}},
			},
			expected: []string{
				"media.download", "video.probe", "video.preprocess",
				"ai.face_swap", "ai.tts", "audio.bgm_select", "audio.mix",
				"ai.subtitle_gen", "video.subtitles",
				"video.encode", "media.upload",
				"quality.video_check", "quality.face_check",
			},
		},
		{
			name: "minimal request",
			req: &core.VideoRequirement{
				QualityLevel: core.QualityDraft,
			},
			expected: []string{"video.encode", "media.upload"},
			excluded: []string{"ai.face_swap", "ai.tts", "quality.video_check"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selected := planner.selectTools(tc.req)
			for _, exp := range tc.expected {
				assert.Contains(t, selected, exp)
			}
			for _, exc := range tc.excluded {
				assert.NotContains(t, selected, exc)
			}
		})
	}
}

func TestFixDAG(t *testing.T) {
	planner := &TaskPlanner{}

	tests := []struct {
		name     string
		input    string
		contains string
		excludes string
	}{
		{
			name:     "plain YAML",
			input:    "name: test\ntasks: {}",
			contains: "name: test",
		},
		{
			name:     "markdown yaml fence",
			input:    "```yaml\nname: test\ntasks: {}\n```",
			contains: "name: test",
			excludes: "```",
		},
		{
			name:     "markdown generic fence",
			input:    "```\nname: test\ntasks: {}\n```",
			contains: "name: test",
			excludes: "```",
		},
		{
			name:     "whitespace padding",
			input:    "  \nname: test\n  ",
			contains: "name: test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := planner.fixDAG(tc.input)
			assert.Contains(t, result, tc.contains)
			if tc.excludes != "" {
				assert.NotContains(t, result, tc.excludes)
			}
		})
	}
}

func TestFaceSwapWithTTSTemplateMatch(t *testing.T) {
	tmpl := FaceSwapWithTTSTemplate()

	req := &core.VideoRequirement{
		FaceSwap:     &core.FaceSwapReq{},
		TTS:          &core.TTSReq{},
		BGM:          &core.BGMReq{},
		Subtitles:    &core.SubtitleReq{},
		SourceVideos: []core.MediaRef{{URL: "test"}},
	}
	assert.True(t, tmpl.Match(req))

	assert.False(t, tmpl.Match(&core.VideoRequirement{
		TTS:          &core.TTSReq{},
		BGM:          &core.BGMReq{},
		Subtitles:    &core.SubtitleReq{},
		SourceVideos: []core.MediaRef{{URL: "test"}},
	}))

	assert.False(t, tmpl.Match(&core.VideoRequirement{
		FaceSwap:     &core.FaceSwapReq{},
		BGM:          &core.BGMReq{},
		Subtitles:    &core.SubtitleReq{},
		SourceVideos: []core.MediaRef{{URL: "test"}},
	}))

	assert.False(t, tmpl.Match(&core.VideoRequirement{
		FaceSwap:  &core.FaceSwapReq{},
		TTS:       &core.TTSReq{},
		BGM:       &core.BGMReq{},
		Subtitles: &core.SubtitleReq{},
	}))
}

func TestFaceSwapWithTTSTemplateBuild(t *testing.T) {
	tmpl := FaceSwapWithTTSTemplate()
	req := &core.VideoRequirement{
		FaceSwap: &core.FaceSwapReq{
			TargetFace: core.MediaRef{URL: "https://face.jpg"},
		},
		TTS:       &core.TTSReq{Text: "script text", Voice: "en-US-Jenny", Language: "en-US"},
		BGM:       &core.BGMReq{Style: "chill", Volume: 0.5},
		Subtitles: &core.SubtitleReq{Language: "en-US"},
		SourceVideos: []core.MediaRef{
			{URL: "https://source.mp4"},
		},
		Resolution: "720p",
	}

	dagYAML := tmpl.Build(req)

	assert.Contains(t, dagYAML, "name: face-swap-with-tts")
	taskCount := strings.Count(dagYAML, "handler:")
	assert.Equal(t, 15, taskCount)

	assert.Contains(t, dagYAML, "https://source.mp4")
	assert.Contains(t, dagYAML, "https://face.jpg")
	assert.Contains(t, dagYAML, "script text")
	assert.Contains(t, dagYAML, "en-US-Jenny")
	assert.Contains(t, dagYAML, "chill")
	assert.Contains(t, dagYAML, "720p")
}
