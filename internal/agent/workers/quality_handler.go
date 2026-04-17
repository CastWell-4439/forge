package workers

import (
	"context"
	"fmt"
	"time"
)

// Quality handler tool definitions and implementations.

// QualityVideoCheckDef returns the ToolDef for quality.video_check.
func QualityVideoCheckDef() *ToolDef {
	return &ToolDef{
		Name:        "quality.video_check",
		DisplayName: "Video Quality Check",
		Category:    "quality",
		Description: "Check video quality metrics: resolution, duration, audio sync, file size. Returns a quality report JSON.",
		InputSchema: map[string]ParamDef{
			"video_path": {Type: "string", Description: "Path to the video to check", Required: true},
			"checks":     {Type: "array", Description: "List of checks: resolution, duration, audio_sync, file_size"},
		},
		OutputSchema: map[string]ParamDef{
			"pass":    {Type: "boolean", Description: "Whether all checks passed"},
			"score":   {Type: "number", Description: "Overall quality score 0.0-1.0"},
			"details": {Type: "object", Description: "Per-check results"},
		},
		RequiredParams:      []string{"video_path"},
		RequiresGPU:         false,
		EstimatedTime:       10 * time.Second,
		TypicalPredecessors: []string{"video.encode"},
		TypicalSuccessors:   []string{"media.upload"},
	}
}

// QualityFaceCheckDef returns the ToolDef for quality.face_check.
func QualityFaceCheckDef() *ToolDef {
	return &ToolDef{
		Name:        "quality.face_check",
		DisplayName: "Face Quality Check",
		Category:    "quality",
		Description: "Check face swap quality by comparing the swapped face against the original target face image. Returns similarity score. Requires GPU.",
		InputSchema: map[string]ParamDef{
			"video_path":           {Type: "string", Description: "Path to the face-swapped video", Required: true},
			"face_image_path":      {Type: "string", Description: "Path to the original target face image", Required: true},
			"similarity_threshold": {Type: "number", Description: "Minimum similarity threshold (default: 0.7)"},
		},
		OutputSchema: map[string]ParamDef{
			"pass":       {Type: "boolean", Description: "Whether face similarity meets threshold"},
			"similarity": {Type: "number", Description: "Face similarity score 0.0-1.0"},
			"details":    {Type: "string", Description: "Detailed face check report"},
		},
		RequiredParams:      []string{"video_path", "face_image_path"},
		RequiresGPU:         true,
		EstimatedTime:       20 * time.Second,
		TypicalPredecessors: []string{"video.encode", "media.download"},
		TypicalSuccessors:   []string{"media.upload"},
	}
}

// NewQualityVideoCheckHandler creates a quality.video_check handler in the given mode.
func NewQualityVideoCheckHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockQualityVideoCheck()
	}
	return realQualityStub("quality.video_check")
}

// NewQualityFaceCheckHandler creates a quality.face_check handler in the given mode.
func NewQualityFaceCheckHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockQualityFaceCheck()
	}
	return realQualityStub("quality.face_check")
}

func mockQualityVideoCheck() HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("quality.video_check: missing required param 'video_path'")
		}

		// Mock: return passing quality report
		details := map[string]interface{}{
			"resolution": map[string]interface{}{
				"pass":   true,
				"actual": "1920x1080",
			},
			"duration": map[string]interface{}{
				"pass":   true,
				"actual": 30.0,
			},
			"audio_sync": map[string]interface{}{
				"pass":     true,
				"offset_ms": 12,
			},
			"file_size": map[string]interface{}{
				"pass":       true,
				"size_bytes": 8388608,
			},
		}

		return map[string]interface{}{
			"pass":    true,
			"score":   0.95,
			"details": details,
		}, nil
	}
}

func mockQualityFaceCheck() HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		videoPath, _ := params["video_path"].(string)
		if videoPath == "" {
			return nil, fmt.Errorf("quality.face_check: missing required param 'video_path'")
		}
		facePath, _ := params["face_image_path"].(string)
		if facePath == "" {
			return nil, fmt.Errorf("quality.face_check: missing required param 'face_image_path'")
		}

		threshold := 0.7
		if t, ok := params["similarity_threshold"].(float64); ok && t > 0 {
			threshold = t
		}

		// Mock: return a similarity score above threshold
		similarity := 0.85
		return map[string]interface{}{
			"pass":       similarity >= threshold,
			"similarity": similarity,
			"details":    fmt.Sprintf("Face similarity %.2f >= threshold %.2f", similarity, threshold),
		}, nil
	}
}

func realQualityStub(name string) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("%s: %w", name, ErrNotConfigured)
	}
}
