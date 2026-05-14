package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Image handler tool definition: image.generate

func ImageGenerateDef() *ToolDef {
	return &ToolDef{
		Name:           "image.generate",
		DisplayName:    "Image Generate",
		Category:       "image",
		Description:    "Generate an image from a text prompt using an AI model (DALL-E, Stable Diffusion, etc.).",
		InputSchema: map[string]ParamDef{
			"prompt": {Type: "string", Description: "Text description of the image to generate", Required: true},
			"width":  {Type: "integer", Description: "Image width in pixels (default 1024)"},
			"height": {Type: "integer", Description: "Image height in pixels (default 1024)"},
			"model":  {Type: "string", Description: "Model to use: dall-e-3, sdxl, flux"},
		},
		OutputSchema: map[string]ParamDef{
			"image_url": {Type: "string", Description: "URL of the generated image"},
			"file_path": {Type: "string", Description: "Local file path if saved"},
		},
		RequiredParams: []string{"prompt"},
		RequiresGPU:    true,
		EstimatedTime:  30 * time.Second,
	}
}

// --- Handlers ---

func NewImageGenerateHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockImageGenerate()
	}
	return realImageGenerate()
}

func mockImageGenerate() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		prompt, _ := params["prompt"].(string)
		if prompt == "" {
			return nil, fmt.Errorf("image.generate: missing required param 'prompt'")
		}
		fakeID := uuid.New().String()[:8]
		return map[string]interface{}{
			"image_url": fmt.Sprintf("https://cdn.example.com/images/%s.png", fakeID),
			"file_path": fmt.Sprintf("/tmp/generated_%s.png", fakeID),
		}, nil
	}
}

func realImageGenerate() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("image.generate: %w", ErrNotConfigured)
	}
}
