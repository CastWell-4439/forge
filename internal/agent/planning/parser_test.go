package planning

import (
	"context"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMClient returns predefined responses for testing.
type mockLLMClient struct {
	responses map[string]string // key: first user message content -> response
	fallback  string            // default response if no match
}

func (m *mockLLMClient) Chat(_ context.Context, messages []core.Message) (string, error) {
	for _, msg := range messages {
		if msg.Role == "user" {
			if resp, ok := m.responses[msg.Content]; ok {
				return resp, nil
			}
		}
	}
	return m.fallback, nil
}

func newMockLLMForParser() *mockLLMClient {
	return &mockLLMClient{
		fallback: `{
	"description": "30秒产品介绍视频，用这张人脸，配轻快的BGM",
	"duration": 30,
	"aspect_ratio": "16:9",
	"resolution": "1080p",
	"face_swap": {
		"target_face": {"url": "https://cdn.example.com/face.jpg", "type": "image", "filename": "face.jpg"},
		"all_faces": false,
		"face_index": [0]
	},
	"tts": {
		"text": "产品介绍脚本",
		"voice": "zh-CN-XiaoxiaoNeural",
		"language": "zh-CN",
		"speed": 1.0
	},
	"bgm": {
		"style": "轻快",
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
}`,
	}
}

func TestRequirementParserParse(t *testing.T) {
	mock := newMockLLMForParser()
	parser := NewRequirementParser(mock)

	req, err := parser.Parse(context.Background(), "帮我做一个30秒的产品介绍视频，用这张人脸，配轻快的BGM")
	require.NoError(t, err)
	require.NotNil(t, req)

	assert.Equal(t, "30秒产品介绍视频，用这张人脸，配轻快的BGM", req.Description)
	assert.Equal(t, float64(30), req.DurationSec)
	assert.Equal(t, "16:9", req.AspectRatio)
	assert.Equal(t, "1080p", req.Resolution)

	// Face swap.
	require.NotNil(t, req.FaceSwap)
	assert.Equal(t, "https://cdn.example.com/face.jpg", req.FaceSwap.TargetFace.URL)
	assert.False(t, req.FaceSwap.AllFaces)

	// TTS.
	require.NotNil(t, req.TTS)
	assert.Equal(t, "zh-CN", req.TTS.Language)

	// BGM.
	require.NotNil(t, req.BGM)
	assert.Equal(t, "轻快", req.BGM.Style)
	assert.Equal(t, 0.3, req.BGM.Volume)

	// Subtitles.
	require.NotNil(t, req.Subtitles)

	// Source videos.
	require.Len(t, req.SourceVideos, 1)
	assert.Equal(t, "https://cdn.example.com/source.mp4", req.SourceVideos[0].URL)

	// Quality.
	assert.Equal(t, core.QualityStandard, req.QualityLevel)
}

func TestRequirementParserDefaults(t *testing.T) {
	mock := &mockLLMClient{
		fallback: `{"duration": 60}`,
	}
	parser := NewRequirementParser(mock)

	req, err := parser.Parse(context.Background(), "make a video")
	require.NoError(t, err)

	assert.Equal(t, "make a video", req.Description) // fallback from input
	assert.Equal(t, "16:9", req.AspectRatio)
	assert.Equal(t, "1080p", req.Resolution)
	assert.Equal(t, core.QualityStandard, req.QualityLevel)
}

func TestRequirementParserMarkdownWrapped(t *testing.T) {
	mock := &mockLLMClient{
		fallback: "```json\n{\"duration\": 30, \"description\": \"test\", \"aspect_ratio\": \"9:16\"}\n```",
	}
	parser := NewRequirementParser(mock)

	req, err := parser.Parse(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, float64(30), req.DurationSec)
	assert.Equal(t, "9:16", req.AspectRatio)
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "pure json",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "json with preamble",
			input:    `Here is the result: {"key": "value"} done`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "nested json",
			input:    `{"outer": {"inner": 1}}`,
			expected: `{"outer": {"inner": 1}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractJSON(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}
