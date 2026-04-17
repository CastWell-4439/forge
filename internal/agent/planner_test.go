package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPlannerTemplateMatch(t *testing.T) {
	mock := &mockLLMClient{fallback: "should not be called"}
	registry := tools.DefaultRegistry()
	planner := NewTaskPlanner(mock, registry)

	req := &VideoRequirement{
		Description: "face swap video with TTS",
		FaceSwap: &FaceSwapReq{
			TargetFace: MediaRef{URL: "https://cdn.example.com/face.jpg", Type: "image"},
		},
		TTS:       &TTSReq{Text: "Hello world", Voice: "zh-CN-XiaoxiaoNeural", Language: "zh-CN"},
		BGM:       &BGMReq{Style: "upbeat", Volume: 0.3},
		Subtitles: &SubtitleReq{Language: "zh-CN"},
		SourceVideos: []MediaRef{
			{URL: "https://cdn.example.com/source.mp4", Type: "video"},
		},
		Resolution: "1080p",
	}

	dagYAML, err := planner.Plan(context.Background(), req)
	require.NoError(t, err)

	// Should use template (not LLM).
	assert.Contains(t, dagYAML, "name: face-swap-with-tts")
	assert.Contains(t, dagYAML, "handler: ai.face_swap")
	assert.Contains(t, dagYAML, "handler: ai.tts")
	assert.Contains(t, dagYAML, "handler: audio.bgm_select")
	assert.Contains(t, dagYAML, "handler: ai.lip_sync")
	assert.Contains(t, dagYAML, "handler: video.subtitles")
	assert.Contains(t, dagYAML, "handler: media.upload")
	assert.Contains(t, dagYAML, "handler: quality.video_check")
	assert.Contains(t, dagYAML, "handler: quality.face_check")

	// Verify params are interpolated.
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
	registry := tools.DefaultRegistry()
	planner := NewTaskPlanner(mock, registry)

	// Requirement that does NOT match any template (no face swap, no TTS, etc.).
	req := &VideoRequirement{
		Description: "trim a video from 5s to 15s",
		SourceVideos: []MediaRef{
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
	registry := tools.DefaultRegistry()
	planner := NewTaskPlanner(mock, registry)

	req := &VideoRequirement{Description: "just download"}

	dagYAML, err := planner.Plan(context.Background(), req)
	require.NoError(t, err)
	assert.Contains(t, dagYAML, "name: test-dag")
	assert.NotContains(t, dagYAML, "```")
}

func TestSelectTools(t *testing.T) {
	registry := tools.DefaultRegistry()
	planner := NewTaskPlanner(nil, registry)

	tests := []struct {
		name     string
		req      *VideoRequirement
		expected []string
		excluded []string
	}{
		{
			name: "face swap with TTS",
			req: &VideoRequirement{
				FaceSwap:     &FaceSwapReq{},
				TTS:          &TTSReq{},
				BGM:          &BGMReq{},
				Subtitles:    &SubtitleReq{},
				QualityLevel: QualityStandard,
				SourceVideos: []MediaRef{{URL: "test"}},
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
			req: &VideoRequirement{
				QualityLevel: QualityDraft,
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

	// Should match.
	req := &VideoRequirement{
		FaceSwap:     &FaceSwapReq{},
		TTS:          &TTSReq{},
		BGM:          &BGMReq{},
		Subtitles:    &SubtitleReq{},
		SourceVideos: []MediaRef{{URL: "test"}},
	}
	assert.True(t, tmpl.Match(req))

	// Missing face swap — no match.
	assert.False(t, tmpl.Match(&VideoRequirement{
		TTS:          &TTSReq{},
		BGM:          &BGMReq{},
		Subtitles:    &SubtitleReq{},
		SourceVideos: []MediaRef{{URL: "test"}},
	}))

	// Missing TTS — no match.
	assert.False(t, tmpl.Match(&VideoRequirement{
		FaceSwap:     &FaceSwapReq{},
		BGM:          &BGMReq{},
		Subtitles:    &SubtitleReq{},
		SourceVideos: []MediaRef{{URL: "test"}},
	}))

	// Missing source videos — no match.
	assert.False(t, tmpl.Match(&VideoRequirement{
		FaceSwap:  &FaceSwapReq{},
		TTS:       &TTSReq{},
		BGM:       &BGMReq{},
		Subtitles: &SubtitleReq{},
	}))
}

func TestFaceSwapWithTTSTemplateBuild(t *testing.T) {
	tmpl := FaceSwapWithTTSTemplate()
	req := &VideoRequirement{
		FaceSwap: &FaceSwapReq{
			TargetFace: MediaRef{URL: "https://face.jpg"},
		},
		TTS:       &TTSReq{Text: "script text", Voice: "en-US-Jenny", Language: "en-US"},
		BGM:       &BGMReq{Style: "chill", Volume: 0.5},
		Subtitles: &SubtitleReq{Language: "en-US"},
		SourceVideos: []MediaRef{
			{URL: "https://source.mp4"},
		},
		Resolution: "720p",
	}

	dagYAML := tmpl.Build(req)

	// Check structure.
	assert.Contains(t, dagYAML, "name: face-swap-with-tts")

	// Check task count: should have 15 tasks.
	taskCount := strings.Count(dagYAML, "handler:")
	assert.Equal(t, 15, taskCount)

	// Check parameter interpolation.
	assert.Contains(t, dagYAML, "https://source.mp4")
	assert.Contains(t, dagYAML, "https://face.jpg")
	assert.Contains(t, dagYAML, "script text")
	assert.Contains(t, dagYAML, "en-US-Jenny")
	assert.Contains(t, dagYAML, "chill")
	assert.Contains(t, dagYAML, "720p")
}
