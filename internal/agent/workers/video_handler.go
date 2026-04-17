package workers

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Video handler tool definitions.

// VideoProbeDef returns the ToolDef for video.probe.
func VideoProbeDef() *ToolDef {
	return &ToolDef{
		Name:        "video.probe",
		DisplayName: "Video Probe",
		Category:    "video",
		Description: "Probe a video file to extract codec information, resolution, duration, and decodability. Uses FFprobe.",
		InputSchema: map[string]ParamDef{
			"video_path":   {Type: "string", Description: "Path to the video file to probe", Required: true},
			"sample_count": {Type: "integer", Description: "Number of sample frames to check"},
		},
		OutputSchema: map[string]ParamDef{
			"codec":      {Type: "string", Description: "Video codec name (e.g. h264)"},
			"width":      {Type: "integer", Description: "Video width in pixels"},
			"height":     {Type: "integer", Description: "Video height in pixels"},
			"duration":   {Type: "number", Description: "Duration in seconds"},
			"fps":        {Type: "number", Description: "Frames per second"},
			"bitrate":    {Type: "integer", Description: "Bitrate in bps"},
			"decodable":  {Type: "boolean", Description: "Whether the video can be decoded"},
			"audio_codec": {Type: "string", Description: "Audio codec name (e.g. aac)"},
		},
		RequiredParams:      []string{"video_path"},
		RequiresGPU:         false,
		EstimatedTime:       1 * time.Second,
		TypicalPredecessors: []string{"media.download"},
		TypicalSuccessors:   []string{"video.preprocess"},
	}
}

// VideoPreprocessDef returns the ToolDef for video.preprocess.
func VideoPreprocessDef() *ToolDef {
	return &ToolDef{
		Name:        "video.preprocess",
		DisplayName: "Video Preprocess",
		Category:    "video",
		Description: "Preprocess (transcode) a video to a standardized format suitable for AI processing. Normalizes codec, resolution, and framerate.",
		InputSchema: map[string]ParamDef{
			"video_path": {Type: "string", Description: "Path to the input video", Required: true},
			"codec":      {Type: "string", Description: "Target codec (default: libx264)"},
			"crf":        {Type: "integer", Description: "Constant Rate Factor for quality (default: 18)"},
			"preset":     {Type: "string", Description: "Encoding preset (default: fast)"},
		},
		OutputSchema: map[string]ParamDef{
			"output_path": {Type: "string", Description: "Path to the preprocessed video"},
			"width":       {Type: "integer", Description: "Output width"},
			"height":      {Type: "integer", Description: "Output height"},
			"duration":    {Type: "number", Description: "Output duration in seconds"},
		},
		RequiredParams:      []string{"video_path"},
		RequiresGPU:         false,
		EstimatedTime:       60 * time.Second,
		TypicalPredecessors: []string{"video.probe"},
		TypicalSuccessors:   []string{"ai.face_swap", "ai.lip_sync"},
	}
}

// NewVideoProbeHandler creates a video.probe handler in the given mode.
func NewVideoProbeHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoProbe()
	}
	return realVideoProbe()
}

// NewVideoPreprocessHandler creates a video.preprocess handler in the given mode.
func NewVideoPreprocessHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockVideoPreprocess(cfg)
	}
	return realVideoPreprocess()
}

func mockVideoProbe() HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("video.probe: missing required param 'video_path'")
		}

		// Mock: return standard 1080p H.264 probe result
		return map[string]interface{}{
			"codec":       "h264",
			"width":       1920,
			"height":      1080,
			"duration":    30.0,
			"fps":         30.0,
			"bitrate":     4500000,
			"decodable":   true,
			"audio_codec": "aac",
			"video_path":  videoPath,
		}, nil
	}
}

func realVideoProbe() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("video.probe: %w", ErrNotConfigured)
	}
}

func mockVideoPreprocess(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("video.preprocess: missing required param 'video_path'")
		}

		outputDir := filepath.Join(cfg.Workspace, "intermediate")
		outputPath := filepath.Join(outputDir, fmt.Sprintf("preprocessed_%s.mp4", uuid.New().String()[:8]))

		return map[string]interface{}{
			"output_path": outputPath,
			"width":       1920,
			"height":      1080,
			"duration":    30.0,
		}, nil
	}
}

func realVideoPreprocess() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("video.preprocess: %w", ErrNotConfigured)
	}
}
