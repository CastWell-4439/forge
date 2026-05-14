package workers

import (
	"context"
	"fmt"
	"time"
)

// LLM handler tool definition: llm.summarize

func LLMSummarizeDef() *ToolDef {
	return &ToolDef{
		Name:           "llm.summarize",
		DisplayName:    "LLM Summarize",
		Category:       "llm",
		Description:    "Use an LLM to summarize text. Useful for condensing long documents or API responses.",
		InputSchema: map[string]ParamDef{
			"text":       {Type: "string", Description: "Text to summarize", Required: true},
			"max_tokens": {Type: "integer", Description: "Max tokens in summary (default 200)"},
			"style":      {Type: "string", Description: "Summary style: brief, detailed, bullet_points"},
		},
		OutputSchema: map[string]ParamDef{
			"summary": {Type: "string", Description: "Generated summary"},
		},
		RequiredParams: []string{"text"},
		EstimatedTime:  5 * time.Second,
	}
}

// --- Handlers ---

func NewLLMSummarizeHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockLLMSummarize()
	}
	return realLLMSummarize()
}

func mockLLMSummarize() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		text, _ := params["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("llm.summarize: missing required param 'text'")
		}
		// Mock: return first 100 chars as "summary"
		summary := text
		if len(summary) > 100 {
			summary = summary[:100] + "..."
		}
		return map[string]interface{}{
			"summary": "[Summary] " + summary,
		}, nil
	}
}

func realLLMSummarize() HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf("llm.summarize: %w", ErrNotConfigured)
	}
}
