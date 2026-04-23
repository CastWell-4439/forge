// Package planning implements requirement parsing, task planning, and DAG
// generation for the Agent layer.
package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/structured"
)

// RequirementParser uses an LLM to parse natural language into a VideoRequirement.
type RequirementParser struct {
	llmClient core.LLMClient
}

// NewRequirementParser creates a new RequirementParser with the given LLM client.
func NewRequirementParser(llm core.LLMClient) *RequirementParser {
	return &RequirementParser{llmClient: llm}
}

// Parse sends the user's text to the LLM and returns a structured VideoRequirement.
func (p *RequirementParser) Parse(ctx context.Context, userText string) (*core.VideoRequirement, error) {
	systemPrompt := `你是一个视频制作需求分析师。根据用户的自然语言描述，提取结构化的视频制作需求。

请输出 JSON 格式的需求分析结果，包含以下字段：
- description: 用户原始描述
- duration: 视频时长（秒）
- aspect_ratio: "16:9" | "9:16" | "1:1"
- resolution: "1080p" | "720p" | "4K"
- face_swap: 是否需要换脸（对象包含 target_face, all_faces, face_index）
- lip_sync: 是否需要口型同步
- tts: 是否需要语音合成（对象包含 text, voice, language, speed）
- bgm: 背景音乐（对象包含 style, volume）
- subtitles: 是否需要字幕
- script: 是否需要AI生成脚本
- source_videos: 源视频引用列表
- source_images: 源图片引用列表
- quality_level: "draft" | "standard" | "premium"

注意：
1. 如果用户没有指定某个参数，使用合理的默认值
2. 分析处理步骤之间的依赖关系
3. 考虑视频处理的实际约束

只输出纯 JSON，不要包含 markdown 代码块或任何解释文字。`

	messages := []core.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userText},
	}

	raw, err := p.llmClient.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("parse requirement: LLM call failed: %w", err)
	}

	// Extract JSON from the response (handles markdown fences, string escapes, etc.).
	jsonStr := structured.ExtractJSONObject(raw)

	var req core.VideoRequirement
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		return nil, fmt.Errorf("parse requirement: invalid JSON from LLM: %w", err)
	}

	// Set duration from DurationSec.
	if req.DurationSec > 0 {
		req.Duration = time.Duration(req.DurationSec) * time.Second
	}

	// Ensure description is set.
	if req.Description == "" {
		req.Description = userText
	}

	// Apply defaults.
	if req.AspectRatio == "" {
		req.AspectRatio = "16:9"
	}
	if req.Resolution == "" {
		req.Resolution = "1080p"
	}
	if req.QualityLevel == "" {
		req.QualityLevel = core.QualityStandard
	}

	return &req, nil
}
