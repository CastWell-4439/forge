// Package agent implements the Agent layer for Forge — requirement parsing,
// task planning, DAG generation, quality checking, and session management.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LLMClient is the interface for communicating with a Large Language Model.
// Implementations can be real API clients or mock clients for testing.
type LLMClient interface {
	// Chat sends messages to the LLM and returns the response text.
	Chat(ctx context.Context, messages []Message) (string, error)
}

// Message represents a single message in an LLM conversation.
type Message struct {
	Role    string `json:"role"`    // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// QualityLevel defines the quality tier for video production.
type QualityLevel string

const (
	QualityDraft    QualityLevel = "draft"
	QualityStandard QualityLevel = "standard"
	QualityPremium  QualityLevel = "premium"
)

// VideoRequirement represents a structured video production requirement
// parsed from natural language input. Copied from agent-tech-spec 3.2.
type VideoRequirement struct {
	// Basic info
	Description string        `json:"description"`
	Duration    time.Duration `json:"-"`
	DurationSec float64      `json:"duration"` // for JSON marshaling
	AspectRatio string        `json:"aspect_ratio"`
	Resolution  string        `json:"resolution"`

	// Content elements
	FaceSwap  *FaceSwapReq `json:"face_swap,omitempty"`
	LipSync   *LipSyncReq  `json:"lip_sync,omitempty"`
	TTS       *TTSReq      `json:"tts,omitempty"`
	BGM       *BGMReq      `json:"bgm,omitempty"`
	Subtitles *SubtitleReq `json:"subtitles,omitempty"`
	Script    *ScriptReq   `json:"script,omitempty"`

	// Source materials
	SourceVideos []MediaRef `json:"source_videos,omitempty"`
	SourceImages []MediaRef `json:"source_images,omitempty"`
	SourceAudios []MediaRef `json:"source_audios,omitempty"`

	// Quality requirements
	QualityLevel QualityLevel `json:"quality_level"`
}

// FaceSwapReq describes a face swap requirement.
type FaceSwapReq struct {
	TargetFace MediaRef `json:"target_face"`
	AllFaces   bool     `json:"all_faces"`
	FaceIndex  []int    `json:"face_index,omitempty"`
}

// LipSyncReq describes a lip sync requirement.
type LipSyncReq struct {
	Mode string `json:"mode"` // "v1" | "v2"
}

// TTSReq describes a text-to-speech requirement.
type TTSReq struct {
	Text     string  `json:"text"`
	Voice    string  `json:"voice"`
	Language string  `json:"language"`
	Speed    float64 `json:"speed"`
}

// BGMReq describes a background music requirement.
type BGMReq struct {
	Style  string   `json:"style"`
	Source MediaRef `json:"source,omitempty"`
	Volume float64  `json:"volume"`
}

// SubtitleReq describes a subtitle requirement.
type SubtitleReq struct {
	Language string `json:"language"`
	Style    string `json:"style"`
	Position string `json:"position"`
}

// ScriptReq describes a script generation requirement.
type ScriptReq struct {
	Topic    string `json:"topic"`
	Style    string `json:"style"`
	Language string `json:"language"`
}

// MediaRef is a reference to a media file.
type MediaRef struct {
	URL      string `json:"url"`
	Type     string `json:"type"`     // "image" | "video" | "audio"
	Filename string `json:"filename"`
}

// ToPromptString formats the requirement as a human-readable string for LLM prompts.
func (r *VideoRequirement) ToPromptString() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r.Description
	}
	return string(data)
}

// RequirementParser uses an LLM to parse natural language into a VideoRequirement.
type RequirementParser struct {
	llmClient LLMClient
}

// NewRequirementParser creates a new RequirementParser with the given LLM client.
func NewRequirementParser(llm LLMClient) *RequirementParser {
	return &RequirementParser{llmClient: llm}
}

// Parse sends the user's text to the LLM and returns a structured VideoRequirement.
func (p *RequirementParser) Parse(ctx context.Context, userText string) (*VideoRequirement, error) {
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

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userText},
	}

	raw, err := p.llmClient.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("parse requirement: LLM call failed: %w", err)
	}

	// Extract JSON from the response (strip markdown if needed).
	jsonStr := extractJSON(raw)

	var req VideoRequirement
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
		req.QualityLevel = QualityStandard
	}

	return &req, nil
}

// extractJSON attempts to extract a JSON object from a potentially
// markdown-wrapped LLM response.
func extractJSON(raw string) string {
	// Try to find JSON block in markdown.
	start := -1
	for i, ch := range raw {
		if ch == '{' {
			start = i
			break
		}
	}
	if start == -1 {
		return raw
	}

	// Find the matching closing brace.
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}

	return raw[start:]
}
