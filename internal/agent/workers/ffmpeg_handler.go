package workers

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// FFmpeg-based handler tool definitions and implementations.

// VideoEncodeDef returns the ToolDef for video.encode.
func VideoEncodeDef() *ToolDef {
	return &ToolDef{
		Name:        "video.encode",
		DisplayName: "Video Encode",
		Category:    "video",
		Description: "Encode/transcode video to final output format with specified codec, quality, and audio settings.",
		InputSchema: map[string]ParamDef{
			"video_path":    {Type: "string", Description: "Input video path", Required: true},
			"codec":         {Type: "string", Description: "Video codec (default: libx264)"},
			"crf":           {Type: "integer", Description: "Constant Rate Factor (default: 20)"},
			"preset":        {Type: "string", Description: "Encoding preset (default: medium)"},
			"audio_codec":   {Type: "string", Description: "Audio codec (default: aac)"},
			"audio_bitrate": {Type: "string", Description: "Audio bitrate (default: 192k)"},
			"format":        {Type: "string", Description: "Output format (default: mp4)"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the encoded video"},
			"file_size":   {Type: "integer", Description: "Output file size in bytes"},
			"duration":    {Type: "number", Description: "Output duration in seconds"},
		},
		RequiredParams:      []string{"video_path"},
		RequiresGPU:         false,
		EstimatedTime:       90 * time.Second,
		TypicalPredecessors: []string{"video.subtitles", "ai.lip_sync", "video.concat"},
		TypicalSuccessors:   []string{"quality.video_check", "media.upload"},
	}
}

// VideoTrimDef returns the ToolDef for video.trim.
func VideoTrimDef() *ToolDef {
	return &ToolDef{
		Name:        "video.trim",
		DisplayName: "Video Trim",
		Category:    "video",
		Description: "Trim a video to a specific time range. Supports precise cutting with re-encoding.",
		InputSchema: map[string]ParamDef{
			"video_path": {Type: "string", Description: "Input video path", Required: true},
			"start_time": {Type: "number", Description: "Start time in seconds", Required: true},
			"end_time":   {Type: "number", Description: "End time in seconds", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the trimmed video"},
			"duration":    {Type: "number", Description: "Trimmed video duration in seconds"},
		},
		RequiredParams:      []string{"video_path", "start_time", "end_time"},
		RequiresGPU:         false,
		EstimatedTime:       15 * time.Second,
		TypicalPredecessors: []string{"video.preprocess", "media.download"},
		TypicalSuccessors:   []string{"video.concat", "ai.face_swap"},
	}
}

// VideoConcatDef returns the ToolDef for video.concat.
func VideoConcatDef() *ToolDef {
	return &ToolDef{
		Name:        "video.concat",
		DisplayName: "Video Concat",
		Category:    "video",
		Description: "Concatenate multiple video segments into a single video.",
		InputSchema: map[string]ParamDef{
			"video_paths": {Type: "array", Description: "Array of video file paths to concatenate", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the concatenated video"},
			"duration":    {Type: "number", Description: "Total duration in seconds"},
		},
		RequiredParams:      []string{"video_paths"},
		RequiresGPU:         false,
		EstimatedTime:       30 * time.Second,
		TypicalPredecessors: []string{"video.trim", "video.preprocess"},
		TypicalSuccessors:   []string{"video.encode", "ai.face_swap"},
	}
}

// VideoSubtitlesDef returns the ToolDef for video.subtitles.
func VideoSubtitlesDef() *ToolDef {
	return &ToolDef{
		Name:        "video.subtitles",
		DisplayName: "Add Subtitles",
		Category:    "video",
		Description: "Burn subtitles into a video from an SRT or ASS subtitle file.",
		InputSchema: map[string]ParamDef{
			"video_path":    {Type: "string", Description: "Input video path", Required: true},
			"subtitle_path": {Type: "string", Description: "Path to SRT/ASS subtitle file", Required: true},
			"style":         {Type: "string", Description: "Subtitle style preset (default, bold, etc.)"},
			"position":      {Type: "string", Description: "Position: top, center, bottom (default: bottom)"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the video with subtitles"},
		},
		RequiredParams:      []string{"video_path", "subtitle_path"},
		RequiresGPU:         false,
		EstimatedTime:       30 * time.Second,
		TypicalPredecessors: []string{"ai.lip_sync", "ai.subtitle_gen"},
		TypicalSuccessors:   []string{"video.encode"},
	}
}

// AudioMixDef returns the ToolDef for audio.mix.
func AudioMixDef() *ToolDef {
	return &ToolDef{
		Name:        "audio.mix",
		DisplayName: "Audio Mix",
		Category:    "audio",
		Description: "Mix multiple audio tracks (e.g. voice + BGM) into a single audio file with individual volume control.",
		InputSchema: map[string]ParamDef{
			"audio_paths": {Type: "array", Description: "Array of audio file paths to mix", Required: true},
			"tracks":      {Type: "array", Description: "Array of {role, volume} track configs"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the mixed audio file"},
			"duration":    {Type: "number", Description: "Duration of the mixed audio in seconds"},
		},
		RequiredParams:      []string{"audio_paths"},
		RequiresGPU:         false,
		EstimatedTime:       15 * time.Second,
		TypicalPredecessors: []string{"ai.tts", "audio.bgm_select"},
		TypicalSuccessors:   []string{"ai.lip_sync", "ai.subtitle_gen"},
	}
}

// AudioBGMSelectDef returns the ToolDef for audio.bgm_select.
func AudioBGMSelectDef() *ToolDef {
	return &ToolDef{
		Name:        "audio.bgm_select",
		DisplayName: "BGM Select",
		Category:    "audio",
		Description: "Intelligently select a background music track from the library based on style description and target duration.",
		InputSchema: map[string]ParamDef{
			"style":            {Type: "string", Description: "Desired BGM style (e.g. upbeat, calm, tech)", Required: true},
			"duration_seconds": {Type: "integer", Description: "Target BGM duration in seconds"},
		},
		OutputSchema: map[string]ParamDef{
			"bgm_path": {Type: "string", Description: "Path to the selected BGM file"},
			"bgm_name": {Type: "string", Description: "Name of the selected BGM track"},
			"duration":  {Type: "number", Description: "Actual BGM duration in seconds"},
		},
		RequiredParams:      []string{"style"},
		RequiresGPU:         false,
		EstimatedTime:       5 * time.Second,
		TypicalPredecessors: nil,
		TypicalSuccessors:   []string{"audio.mix"},
	}
}

// --- Handler constructors ---

// NewVideoEncodeHandler creates a video.encode handler in the given mode.
func NewVideoEncodeHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoEncode(cfg)
	}
	return realFFmpegStub("video.encode")
}

// NewVideoTrimHandler creates a video.trim handler in the given mode.
func NewVideoTrimHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoTrim(cfg)
	}
	return realFFmpegStub("video.trim")
}

// NewVideoConcatHandler creates a video.concat handler in the given mode.
func NewVideoConcatHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoConcat(cfg)
	}
	return realFFmpegStub("video.concat")
}

// NewVideoSubtitlesHandler creates a video.subtitles handler in the given mode.
func NewVideoSubtitlesHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoSubtitles(cfg)
	}
	return realFFmpegStub("video.subtitles")
}

// NewAudioMixHandler creates an audio.mix handler in the given mode.
func NewAudioMixHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAudioMix(cfg)
	}
	return realFFmpegStub("audio.mix")
}

// NewAudioBGMSelectHandler creates an audio.bgm_select handler in the given mode.
func NewAudioBGMSelectHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAudioBGMSelect(cfg)
	}
	return realFFmpegStub("audio.bgm_select")
}

// --- Mock implementations ---

func mockVideoEncode(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("video.encode: missing required param 'video_path'")
		}

		format := "mp4"
		if f, ok := params["format"].(string); ok && f != "" {
			format = f
		}

		outputPath := filepath.Join(cfg.Workspace, "output",
			fmt.Sprintf("encoded_%s.%s", uuid.New().String()[:8], format))
		return map[string]interface{}{
			"output_path": outputPath,
			"file_size":   int64(8388608), // 8 MB
			"duration":    30.0,
		}, nil
	}
}

func mockVideoTrim(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("video.trim: missing required param 'video_path'")
		}

		startTime, _ := params["start_time"].(float64)
		endTime, _ := params["end_time"].(float64)
		if endTime <= startTime {
			return nil, fmt.Errorf("video.trim: end_time must be greater than start_time")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("trimmed_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path": outputPath,
			"duration":    endTime - startTime,
		}, nil
	}
}

func mockVideoConcat(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		paths, _ := params["video_paths"].([]interface{})
		if len(paths) == 0 {
			return nil, fmt.Errorf("video.concat: missing required param 'video_paths'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("concat_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path": outputPath,
			"duration":    float64(len(paths)) * 15.0, // ~15s per segment
		}, nil
	}
}

func mockVideoSubtitles(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("video.subtitles: missing required param 'video_path'")
		}
		subtitlePath, _ := params["subtitle_path"].(string)
		if subtitlePath == "" {
			return nil, fmt.Errorf("video.subtitles: missing required param 'subtitle_path'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("subtitled_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path": outputPath,
		}, nil
	}
}

func mockAudioMix(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		audioPaths, _ := params["audio_paths"].([]interface{})
		if len(audioPaths) == 0 {
			return nil, fmt.Errorf("audio.mix: missing required param 'audio_paths'")
		}

		outputPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("mixed_%s.wav", uuid.New().String()[:8]))
		return map[string]interface{}{
			"output_path": outputPath,
			"duration":    30.0,
		}, nil
	}
}

func mockAudioBGMSelect(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		style, _ := params["style"].(string)
		if style == "" {
			return nil, fmt.Errorf("audio.bgm_select: missing required param 'style'")
		}

		bgmPath := filepath.Join(cfg.Workspace, "intermediate",
			fmt.Sprintf("bgm_%s.mp3", uuid.New().String()[:8]))
		return map[string]interface{}{
			"bgm_path": bgmPath,
			"bgm_name": fmt.Sprintf("Upbeat_%s_Mix", style),
			"duration":  35.0,
		}, nil
	}
}

// realFFmpegStub returns a real-mode handler that returns ErrNotConfigured.
func realFFmpegStub(name string) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("%s: %w", name, ErrNotConfigured)
	}
}
