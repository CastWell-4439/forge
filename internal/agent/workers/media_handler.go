package workers

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Media handler tool definitions.

// MediaDownloadDef returns the ToolDef for media.download.
func MediaDownloadDef() *ToolDef {
	return &ToolDef{
		Name:        "media.download",
		DisplayName: "Media Download",
		Category:    "media",
		Description: "Download a media file from a URL to local storage. Returns the local file path.",
		InputSchema: map[string]ParamDef{
			"url":        {Type: "string", Description: "Source URL to download from", Required: true},
			"output_dir": {Type: "string", Description: "Local directory to save the file"},
		},
		OutputSchema: map[string]ParamDef{
			"file_path": {Type: "string", Description: "Local path of the downloaded file"},
			"file_size": {Type: "integer", Description: "File size in bytes"},
		},
		RequiredParams:      []string{"url"},
		RequiresGPU:         false,
		EstimatedTime:       15 * time.Second,
		TypicalPredecessors: nil,
		TypicalSuccessors:   []string{"video.probe", "ai.face_swap"},
	}
}

// MediaUploadDef returns the ToolDef for media.upload.
func MediaUploadDef() *ToolDef {
	return &ToolDef{
		Name:        "media.upload",
		DisplayName: "Media Upload",
		Category:    "media",
		Description: "Upload a local media file to OSS/CDN. Returns the public CDN URL.",
		InputSchema: map[string]ParamDef{
			"file_path": {Type: "string", Description: "Local file path to upload", Required: true},
			"bucket":    {Type: "string", Description: "Target OSS bucket name"},
			"acl":       {Type: "string", Description: "Access control: public-read or private"},
		},
		OutputSchema: map[string]ParamDef{
			"url":       {Type: "string", Description: "CDN URL of the uploaded file"},
			"file_size": {Type: "integer", Description: "Uploaded file size in bytes"},
		},
		RequiredParams:      []string{"file_path"},
		RequiresGPU:         false,
		EstimatedTime:       15 * time.Second,
		TypicalPredecessors: []string{"video.encode"},
		TypicalSuccessors:   nil,
	}
}

// NewMediaDownloadHandler creates a media.download handler in the given mode.
func NewMediaDownloadHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockMediaDownload(cfg)
	}
	return realMediaDownload(cfg)
}

// NewMediaUploadHandler creates a media.upload handler in the given mode.
func NewMediaUploadHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockMediaUpload(cfg)
	}
	return realMediaUpload(cfg)
}

func mockMediaDownload(cfg HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		url, _ := params["url"].(string)
		if url == "" {
			return nil, fmt.Errorf("media.download: missing required param 'url'")
		}

		outputDir, _ := params["output_dir"].(string)
		if outputDir == "" {
			outputDir = filepath.Join(cfg.Workspace, "input")
		}

		// Mock: generate a fake local file path
		fakeFile := filepath.Join(outputDir, fmt.Sprintf("downloaded_%s.mp4", uuid.New().String()[:8]))
		return map[string]interface{}{
			"file_path": fakeFile,
			"file_size": int64(15728640), // 15 MB
		}, nil
	}
}

func realMediaDownload(_ HandlerConfig) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("media.download: %w", ErrNotConfigured)
	}
}

func mockMediaUpload(_ HandlerConfig) HandlerFunc {
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		filePath, _ := params["file_path"].(string)
		if filePath == "" {
			return nil, fmt.Errorf("media.upload: missing required param 'file_path'")
		}

		// Mock: generate a fake CDN URL
		fakeURL := fmt.Sprintf("https://cdn.example.com/output/%s.mp4", uuid.New().String()[:8])
		return map[string]interface{}{
			"url":       fakeURL,
			"file_size": int64(10485760), // 10 MB
		}, nil
	}
}

func realMediaUpload(_ HandlerConfig) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("media.upload: %w", ErrNotConfigured)
	}
}
