package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/workers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRegistry(t *testing.T) {
	reg := DefaultRegistry()
	tools := reg.List()

	// Phase A1 registered exactly 18 tools.
	assert.Len(t, tools, 18)

	// Verify tools are sorted by name.
	for i := 1; i < len(tools); i++ {
		assert.True(t, tools[i-1].Name < tools[i].Name,
			"expected sorted order: %s < %s", tools[i-1].Name, tools[i].Name)
	}
}

func TestRegistryGet(t *testing.T) {
	reg := DefaultRegistry()

	tool := reg.Get("ai.face_swap")
	require.NotNil(t, tool)
	assert.Equal(t, "ai.face_swap", tool.Name)
	assert.True(t, tool.RequiresGPU)

	// Non-existent tool.
	assert.Nil(t, reg.Get("nonexistent"))
}

func TestRegistryHasHandler(t *testing.T) {
	reg := DefaultRegistry()

	assert.True(t, reg.HasHandler("media.download"))
	assert.True(t, reg.HasHandler("quality.face_check"))
	assert.False(t, reg.HasHandler("nonexistent"))
}

func TestRegistryList(t *testing.T) {
	reg := DefaultRegistry()

	// Verify all 18 expected handler names are present.
	expected := []string{
		"ai.face_swap", "ai.lip_sync", "ai.multi_face_swap",
		"ai.script", "ai.subtitle_gen", "ai.tts",
		"audio.bgm_select", "audio.mix",
		"media.download", "media.upload",
		"quality.face_check", "quality.video_check",
		"video.concat", "video.encode", "video.preprocess",
		"video.probe", "video.subtitles", "video.trim",
	}

	names := make([]string, 0, len(reg.List()))
	for _, tool := range reg.List() {
		names = append(names, tool.Name)
	}
	assert.Equal(t, expected, names)
}

func TestFormatForPrompt(t *testing.T) {
	reg := DefaultRegistry()
	prompt := reg.FormatForPrompt()

	// Should contain all handler names.
	assert.Contains(t, prompt, "ai.face_swap")
	assert.Contains(t, prompt, "media.download")
	assert.Contains(t, prompt, "video.encode")

	// Should contain parameter descriptions.
	assert.Contains(t, prompt, "Input parameters:")
	assert.Contains(t, prompt, "required")

	// Should contain category info.
	assert.Contains(t, prompt, "Category:")

	// Should not be empty.
	assert.True(t, len(prompt) > 100)
}

func TestFindSimilar(t *testing.T) {
	reg := DefaultRegistry()

	tests := []struct {
		input    string
		expected string
	}{
		{"ai.face", "ai.face_swap"},
		{"video.enc", "video.encode"},
		{"quality.vid", "quality.video_check"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := reg.FindSimilar(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}

	// Category match: "ai.faceswap" should match an ai.* tool.
	result := reg.FindSimilar("ai.faceswap")
	assert.True(t, strings.HasPrefix(result, "ai."), "expected ai.* category match, got %s", result)
}

func TestFormatForPromptStructure(t *testing.T) {
	reg := DefaultRegistry()
	prompt := reg.FormatForPrompt()

	// Each tool block should start with "### handler.name"
	lines := strings.Split(prompt, "\n")
	headerCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "### ") {
			headerCount++
		}
	}
	assert.Equal(t, 18, headerCount)
}

func TestNewToolRegistryWrapsInner(t *testing.T) {
	inner := workers.NewToolRegistry()
	reg := NewToolRegistry(inner)

	// Initially empty.
	assert.Len(t, reg.List(), 0)
	assert.False(t, reg.HasHandler("test"))

	// Register a tool through the wrapper with a proper HandlerFunc.
	handler := workers.HandlerFunc(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	err := reg.Register(&workers.ToolDef{
		Name:        "test.tool",
		DisplayName: "Test Tool",
		Category:    "test",
		Description: "A test tool",
	}, handler)
	require.NoError(t, err)
	assert.Len(t, reg.List(), 1)
	assert.True(t, reg.HasHandler("test.tool"))
	assert.Equal(t, "Test Tool", reg.Get("test.tool").DisplayName)
}

func TestFindSimilarEmptyRegistry(t *testing.T) {
	inner := workers.NewToolRegistry()
	reg := NewToolRegistry(inner)

	result := reg.FindSimilar("anything")
	assert.Equal(t, "", result)
}
