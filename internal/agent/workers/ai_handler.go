package workers

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// AI handler tool definitions and implementations.

// AIFaceSwapDef returns the ToolDef for ai.face_swap.
func AIFaceSwapDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.face_swap",
		DisplayName: "AI Face Swap",
		Category:    "ai",
		Description: "Replace a face in a video using FaceFusion. Input a video and target face image, output a face-swapped video. Supports specifying which face to replace. Requires GPU.",
		InputSchema: map[string]ParamDef{
			"video_path":      {Type: "string", Description: "Input video local path", Required: true},
			"face_image_path": {Type: "string", Description: "Target face image path", Required: true},
			"face_index":      {Type: "integer", Description: "Which face to replace (0-based), default 0"},
			"face_distance":   {Type: "number", Description: "Face matching threshold, default 0.6"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path":    {Type: "string", Description: "Path to the face-swapped video"},
			"faces_detected": {Type: "integer", Description: "Number of faces detected in the video"},
			"faces_swapped":  {Type: "integer", Description: "Number of faces actually swapped"},
		},
		RequiredParams:      []string{"video_path", "face_image_path"},
		RequiresGPU:         true,
		EstimatedTime:       180 * time.Second,
		MaxInputSize:        500 * 1024 * 1024, // 500 MB
		TypicalPredecessors: []string{"video.preprocess", "media.download"},
		TypicalSuccessors:   []string{"ai.lip_sync", "video.encode"},
	}
}

// AIMultiFaceSwapDef returns the ToolDef for ai.multi_face_swap.
func AIMultiFaceSwapDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.multi_face_swap",
		DisplayName: "AI Multi-Face Swap",
		Category:    "ai",
		Description: "Replace multiple faces in a video. Each face mapping specifies a source face image and target face index. Requires GPU.",
		InputSchema: map[string]ParamDef{
			"video_path":   {Type: "string", Description: "Input video local path", Required: true},
			"face_mappings": {Type: "array", Description: "Array of {face_image_path, face_index} objects", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"output_path":    {Type: "string", Description: "Path to the face-swapped video"},
			"faces_detected": {Type: "integer", Description: "Number of faces detected"},
			"faces_swapped":  {Type: "integer", Description: "Number of faces swapped"},
		},
		RequiredParams:      []string{"video_path", "face_mappings"},
		RequiresGPU:         true,
		EstimatedTime:       480 * time.Second,
		MaxInputSize:        500 * 1024 * 1024,
		TypicalPredecessors: []string{"video.preprocess", "media.download"},
		TypicalSuccessors:   []string{"ai.lip_sync", "video.encode"},
	}
}

// AILipSyncDef returns the ToolDef for ai.lip_sync.
func AILipSyncDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.lip_sync",
		DisplayName: "AI Lip Sync",
		Category:    "ai",
		Description: "Synchronize lip movements in a video to match an audio track. Requires GPU.",
		InputSchema: map[string]ParamDef{
			"video_path": {Type: "string", Description: "Input video path", Required: true},
			"audio_path": {Type: "string", Description: "Audio track to sync lips to", Required: true},
			"mode":       {Type: "string", Description: "Lip sync mode: v1 or v2"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the lip-synced video"},
		},
		RequiredParams:      []string{"video_path", "audio_path"},
		RequiresGPU:         true,
		EstimatedTime:       180 * time.Second,
		TypicalPredecessors: []string{"ai.face_swap", "audio.mix"},
		TypicalSuccessors:   []string{"video.subtitles", "video.encode"},
	}
}

// AITTSDef returns the ToolDef for ai.tts.
func AITTSDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.tts",
		DisplayName: "Text-to-Speech",
		Category:    "ai",
		Description: "Convert text to speech audio using a TTS service. Supports various voices and languages.",
		InputSchema: map[string]ParamDef{
			"text":          {Type: "string", Description: "Text to convert to speech", Required: true},
			"voice":         {Type: "string", Description: "Voice style/name (e.g. zh-CN-XiaoxiaoNeural)"},
			"language":      {Type: "string", Description: "Language code (e.g. zh-CN)"},
			"speed":         {Type: "number", Description: "Speech speed multiplier, default 1.0"},
			"output_format": {Type: "string", Description: "Audio format: wav, mp3"},
		},
		OutputSchema: map[string]ParamDef{
			"audio_path": {Type: "string", Description: "Path to the generated audio file"},
			"duration":   {Type: "number", Description: "Audio duration in seconds"},
		},
		RequiredParams:      []string{"text"},
		RequiresGPU:         false,
		EstimatedTime:       15 * time.Second,
		TypicalPredecessors: []string{"ai.script"},
		TypicalSuccessors:   []string{"audio.mix", "ai.lip_sync"},
	}
}

// AIScriptDef returns the ToolDef for ai.script.
func AIScriptDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.script",
		DisplayName: "AI Script Generator",
		Category:    "ai",
		Description: "Generate a video script using an LLM. Outputs structured text suitable for TTS.",
		InputSchema: map[string]ParamDef{
			"topic":            {Type: "string", Description: "Video topic/subject", Required: true},
			"duration_seconds": {Type: "integer", Description: "Target video duration in seconds", Required: true},
			"style":            {Type: "string", Description: "Script style (e.g. professional, casual)"},
			"language":         {Type: "string", Description: "Language code (e.g. zh-CN)"},
		},
		OutputSchema: map[string]ParamDef{
			"script_text": {Type: "string", Description: "Generated script text"},
			"word_count":  {Type: "integer", Description: "Word count of the script"},
		},
		RequiredParams:      []string{"topic", "duration_seconds"},
		RequiresGPU:         false,
		EstimatedTime:       10 * time.Second,
		TypicalPredecessors: nil,
		TypicalSuccessors:   []string{"ai.tts"},
	}
}

// AISubtitleGenDef returns the ToolDef for ai.subtitle_gen.
func AISubtitleGenDef() *ToolDef {
	return &ToolDef{
		Name:        "ai.subtitle_gen",
		DisplayName: "AI Subtitle Generator",
		Category:    "ai",
		Description: "Generate SRT subtitles from audio/video using ASR (Automatic Speech Recognition). Requires GPU.",
		InputSchema: map[string]ParamDef{
			"media_path": {Type: "string", Description: "Path to audio or video file", Required: true},
			"language":   {Type: "string", Description: "Language code for ASR (e.g. zh-CN)"},
		},
		OutputSchema: map[string]ParamDef{
			"subtitle_path": {Type: "string", Description: "Path to the generated SRT file"},
			"segment_count": {Type: "integer", Description: "Number of subtitle segments"},
		},
		RequiredParams:      []string{"media_path"},
		RequiresGPU:         true,
		EstimatedTime:       60 * time.Second,
		TypicalPredecessors: []string{"audio.mix", "ai.tts"},
		TypicalSuccessors:   []string{"video.subtitles"},
	}
}

// NewAIFaceSwapHandler creates an ai.face_swap handler in the given mode.
func NewAIFaceSwapHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAIFaceSwap(cfg)
	}
	return realAIFaceSwap()
}

// NewAIMultiFaceSwapHandler creates an ai.multi_face_swap handler in the given mode.
func NewAIMultiFaceSwapHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAIMultiFaceSwap(cfg)
	}
	return realAIMultiFaceSwap()
}

// NewAILipSyncHandler creates an ai.lip_sync handler in the given mode.
func NewAILipSyncHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAILipSync(cfg)
	}
	return realAILipSync()
}

// NewAITTSHandler creates an ai.tts handler in the given mode.
func NewAITTSHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAITTS(cfg)
	}
	return realAITTS()
}

// NewAIScriptHandler creates an ai.script handler in the given mode.
func NewAIScriptHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAIScript()
	}
	return realAIScript()
}

// NewAISubtitleGenHandler creates an ai.subtitle_gen handler in the given mode.
func NewAISubtitleGenHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAISubtitleGen(cfg)
	}
	return realAISubtitleGen()
}

// --- Mock implementations ---

func mockAIFaceSwap(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("ai.face_swap: missing required param 'video_path'")
		}
		facePath, _ := params["face_image_path"].(string)
		if facePath == "" {
			return nil, fmt.Errorf("ai.face_swap: missing required param 'face_image_path'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("faceswap_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path":    outputPath,
			"faces_detected": 3,
			"faces_swapped":  1,
		}, nil
	}
}

func mockAIMultiFaceSwap(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("ai.multi_face_swap: missing required param 'video_path'")
		}
		mappings, _ := params["face_mappings"].([]interface{})
		if len(mappings) == 0 {
			return nil, fmt.Errorf("ai.multi_face_swap: missing required param 'face_mappings'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("multi_faceswap_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path":    outputPath,
			"faces_detected": 4,
			"faces_swapped":  len(mappings),
		}, nil
	}
}

func mockAILipSync(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("ai.lip_sync: missing required param 'video_path'")
		}
		audioPath, _ := params["audio_path"].(string)
		if audioPath == "" {
			return nil, fmt.Errorf("ai.lip_sync: missing required param 'audio_path'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("lipsync_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path": outputPath,
		}, nil
	}
}

func mockAITTS(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		text, _ := params["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("ai.tts: missing required param 'text'")
		}

		outputFormat := "wav"
		if f, ok := params["output_format"].(string); ok && f != "" {
			outputFormat = f
		}

		audioPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("tts_%s.%s", uuid.New().String()[:8], outputFormat))

		// Estimate duration: ~3 characters per second for Chinese
		estimatedDuration := float64(len([]rune(text))) / 3.0
		if estimatedDuration < 1.0 {
			estimatedDuration = 1.0
		}

		return map[string]interface{}{
			"audio_path": audioPath,
			"duration":   estimatedDuration,
		}, nil
	}
}

func mockAIScript() HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		topic, _ := params["topic"].(string)
		if topic == "" {
			return nil, fmt.Errorf("ai.script: missing required param 'topic'")
		}

		durationSec := 30
		if d, ok := params["duration_seconds"].(float64); ok {
			durationSec = int(d)
		}

		// Mock: generate plausible script text
		scriptText := fmt.Sprintf(
			"Welcome to our presentation about %s. "+
				"In the next %d seconds, we will explore the key features and benefits. "+
				"Let's get started with an overview of what makes this product unique.",
			topic, durationSec)

		return map[string]interface{}{
			"script_text": scriptText,
			"word_count":  len([]rune(scriptText)),
		}, nil
	}
}

func mockAISubtitleGen(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		mediaPath, _ := params["media_path"].(string)
		if mediaPath == "" {
			return nil, fmt.Errorf("ai.subtitle_gen: missing required param 'media_path'")
		}

		subtitlePath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("subtitles_%s.srt", uuid.New().String()[:8]))
		return map[string]interface{}{
			"subtitle_path": subtitlePath,
			"segment_count": 12,
		}, nil
	}
}

// --- Real implementations (stub — return ErrNotConfigured) ---

func realAIFaceSwap() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.face_swap: %w", ErrNotConfigured)
	}
}

func realAIMultiFaceSwap() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.multi_face_swap: %w", ErrNotConfigured)
	}
}

func realAILipSync() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.lip_sync: %w", ErrNotConfigured)
	}
}

func realAITTS() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.tts: %w", ErrNotConfigured)
	}
}

func realAIScript() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.script: %w", ErrNotConfigured)
	}
}

func realAISubtitleGen() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("ai.subtitle_gen: %w", ErrNotConfigured)
	}
}
