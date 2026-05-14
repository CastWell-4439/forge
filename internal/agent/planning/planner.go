// Package planning implements requirement parsing, task planning, and DAG
// generation for the Agent layer.
package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
	"gopkg.in/yaml.v3"
)

// dagYAML is the top-level struct for generating DAG YAML via yaml.Marshal.
// This replaces the unsafe fmt.Sprintf approach (#6).
type dagYAML struct {
	Name  string                   `yaml:"name"`
	Tasks map[string]taskYAML      `yaml:"tasks"`
}

// taskYAML represents one task in the DAG template.
type taskYAML struct {
	Handler   string                 `yaml:"handler"`
	Params    map[string]interface{} `yaml:"params"`
	DependsOn []string               `yaml:"depends_on,omitempty"`
	Timeout   string                 `yaml:"timeout,omitempty"`
	Retry     *retryYAML             `yaml:"retry,omitempty"`
}

// retryYAML holds retry configuration for a task.
type retryYAML struct {
	MaxAttempts     int    `yaml:"max_attempts"`
	Backoff         string `yaml:"backoff"`
	InitialInterval string `yaml:"initial_interval"`
}

// DAGTemplate is a predefined DAG template for common video production
// scenarios. Strategy A from agent-tech-spec 3.3.
type DAGTemplate struct {
	// Name is the template identifier.
	Name string
	// Description explains what this template is for.
	Description string
	// Match returns true if the given requirement fits this template.
	Match func(req *core.VideoRequirement) bool
	// Build generates a YAML DAG string from the requirement.
	Build func(req *core.VideoRequirement) string
}

// TaskPlanner converts structured VideoRequirements into Forge DAG YAML.
// It uses a two-strategy approach: template matching first, then LLM fallback.
type TaskPlanner struct {
	llmClient core.LLMClient
	registry  *core.ToolRegistry
	templates []DAGTemplate
}

// NewTaskPlanner creates a new TaskPlanner.
func NewTaskPlanner(llm core.LLMClient, registry *core.ToolRegistry) *TaskPlanner {
	p := &TaskPlanner{
		llmClient: llm,
		registry:  registry,
	}
	p.templates = defaultTemplates()
	return p
}

// Plan generates a DAG YAML string for the given requirement.
// It checks templates first, then falls back to LLM generation.
func (p *TaskPlanner) Plan(ctx context.Context, req *core.VideoRequirement) (string, error) {
	// Strategy A: try template matching first (fast, stable).
	for _, tmpl := range p.templates {
		if tmpl.Match(req) {
			return tmpl.Build(req), nil
		}
	}

	// Strategy B: LLM dynamic generation (flexible, complex scenarios).
	return p.planWithLLM(ctx, req)
}

// planWithLLM uses the LLM to dynamically generate a DAG.
func (p *TaskPlanner) planWithLLM(ctx context.Context, req *core.VideoRequirement) (string, error) {
	selectedTools := p.selectTools(req)
	toolsPrompt := p.registry.FormatForPrompt()

	reqJSON, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", fmt.Errorf("plan with LLM: marshal requirement: %w", err)
	}

	systemPrompt := fmt.Sprintf(`你是一个视频处�?DAG 编排专家。根据用户的视频制作需求，生成 Forge DAG YAML�?
规则�?1. 每个 task 必须指定 handler �?params
2. depends_on 必须引用已存在的 task 名称
3. 没有依赖�?task 将并行执�?4. DAG 必须包含 name 字段
5. 每个 task �?handler 必须是以下可�?handler 之一

可用 handler 列表�?%s

推荐使用�?handler（根据需求分析）�?%s

只输出纯 YAML，不要包�?markdown 代码块或任何解释文字。`, toolsPrompt, strings.Join(selectedTools, ", "))

	messages := []core.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(reqJSON)},
	}

	raw, err := p.llmClient.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("plan with LLM: LLM call failed: %w", err)
	}

	return p.fixDAG(raw), nil
}

// selectTools analyzes the requirement and returns a list of recommended
// handler names that should be used in the DAG.
func (p *TaskPlanner) selectTools(req *core.VideoRequirement) []string {
	var selected []string

	// Source material handling: always need download.
	if len(req.SourceVideos) > 0 || len(req.SourceImages) > 0 || len(req.SourceAudios) > 0 {
		selected = append(selected, "media.download")
	}

	// Video processing: probe and preprocess are almost always needed.
	if len(req.SourceVideos) > 0 {
		selected = append(selected, "video.probe", "video.preprocess")
	}

	// Face swap.
	if req.FaceSwap != nil {
		selected = append(selected, "ai.face_swap")
	}

	// Lip sync.
	if req.LipSync != nil {
		selected = append(selected, "ai.lip_sync")
	}

	// TTS.
	if req.TTS != nil {
		selected = append(selected, "ai.tts")
	}

	// Script generation.
	if req.Script != nil {
		selected = append(selected, "ai.script")
	}

	// BGM.
	if req.BGM != nil {
		selected = append(selected, "audio.bgm_select", "audio.mix")
	}

	// Subtitles.
	if req.Subtitles != nil {
		selected = append(selected, "ai.subtitle_gen", "video.subtitles")
	}

	// Encoding and upload are almost always needed.
	selected = append(selected, "video.encode", "media.upload")

	// Quality checks based on quality level.
	if req.QualityLevel == core.QualityStandard || req.QualityLevel == core.QualityPremium {
		selected = append(selected, "quality.video_check")
	}
	if req.FaceSwap != nil && (req.QualityLevel == core.QualityStandard || req.QualityLevel == core.QualityPremium) {
		selected = append(selected, "quality.face_check")
	}

	return selected
}

// fixDAG performs basic cleanup on LLM-generated DAG YAML.
// Strips markdown fences and leading/trailing whitespace.
func (p *TaskPlanner) fixDAG(raw string) string {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences: ```yaml ... ``` or ``` ... ```
	if strings.HasPrefix(s, "```") {
		// Remove first line (```yaml or ```)
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Remove trailing ```
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	return s
}

// defaultTemplates returns the built-in DAG templates.
func defaultTemplates() []DAGTemplate {
	return []DAGTemplate{
		FaceSwapWithTTSTemplate(),
	}
}

// FaceSwapWithTTSTemplate returns a DAG template for the common face-swap
// with TTS, BGM, and subtitles scenario. From agent-tech-spec 3.3.
func FaceSwapWithTTSTemplate() DAGTemplate {
	return DAGTemplate{
		Name:        "face_swap_with_tts",
		Description: "Face swap video with TTS narration, BGM, and subtitles",
		Match: func(req *core.VideoRequirement) bool {
			return req.FaceSwap != nil &&
				req.TTS != nil &&
				req.BGM != nil &&
				req.Subtitles != nil &&
				len(req.SourceVideos) > 0
		},
		Build: buildFaceSwapWithTTS,
	}
}

// buildFaceSwapWithTTS generates DAG YAML using struct + yaml.Marshal (#6 fix).
func buildFaceSwapWithTTS(req *core.VideoRequirement) string {
	sourceURL := ""
	if len(req.SourceVideos) > 0 {
		sourceURL = req.SourceVideos[0].URL
	}
	faceURL := ""
	if req.FaceSwap != nil {
		faceURL = req.FaceSwap.TargetFace.URL
	}
	ttsText := ""
	ttsVoice := "zh-CN-XiaoxiaoNeural"
	ttsLang := "zh-CN"
	if req.TTS != nil {
		ttsText = req.TTS.Text
		if req.TTS.Voice != "" {
			ttsVoice = req.TTS.Voice
		}
		if req.TTS.Language != "" {
			ttsLang = req.TTS.Language
		}
	}
	bgmStyle := "upbeat"
	bgmVolume := 0.3
	if req.BGM != nil {
		if req.BGM.Style != "" {
			bgmStyle = req.BGM.Style
		}
		if req.BGM.Volume > 0 {
			bgmVolume = req.BGM.Volume
		}
	}
	resolution := req.Resolution
	if resolution == "" {
		resolution = "1080p"
	}

	dag := dagYAML{
		Name: "face-swap-with-tts",
		Tasks: map[string]taskYAML{
			"download-source-video": {
				Handler: "media.download",
				Params:  map[string]interface{}{"url": sourceURL},
				Timeout: "60s",
			},
			"download-face-image": {
				Handler: "media.download",
				Params:  map[string]interface{}{"url": faceURL},
				Timeout: "60s",
			},
			"probe-video": {
				Handler:   "video.probe",
				Params:    map[string]interface{}{"video_path": "${download-source-video.output_path}"},
				DependsOn: []string{"download-source-video"},
				Timeout:   "10s",
			},
			"preprocess-video": {
				Handler:   "video.preprocess",
				Params:    map[string]interface{}{"video_path": "${probe-video.video_path}"},
				DependsOn: []string{"probe-video"},
				Timeout:   "300s",
			},
			"face-swap": {
				Handler: "ai.face_swap",
				Params: map[string]interface{}{
					"video_path":      "${preprocess-video.output_path}",
					"face_image_path": "${download-face-image.output_path}",
					"face_index":      0,
				},
				DependsOn: []string{"preprocess-video", "download-face-image"},
				Timeout:   "600s",
				Retry:     &retryYAML{MaxAttempts: 2, Backoff: "exponential", InitialInterval: "10s"},
			},
			"generate-tts": {
				Handler: "ai.tts",
				Params:  map[string]interface{}{"text": ttsText, "voice": ttsVoice, "language": ttsLang},
				Timeout: "60s",
			},
			"select-bgm": {
				Handler: "audio.bgm_select",
				Params:  map[string]interface{}{"style": bgmStyle},
				Timeout: "30s",
			},
			"mix-audio": {
				Handler: "audio.mix",
				Params: map[string]interface{}{
					"audio_paths": []string{"${generate-tts.output_path}", "${select-bgm.output_path}"},
					"bgm_volume":  bgmVolume,
				},
				DependsOn: []string{"generate-tts", "select-bgm"},
				Timeout:   "60s",
			},
			"lip-sync": {
				Handler: "ai.lip_sync",
				Params: map[string]interface{}{
					"video_path": "${face-swap.output_path}",
					"audio_path": "${mix-audio.output_path}",
				},
				DependsOn: []string{"face-swap", "mix-audio"},
				Timeout:   "300s",
			},
			"generate-subtitles": {
				Handler:   "ai.subtitle_gen",
				Params:    map[string]interface{}{"media_path": "${mix-audio.output_path}"},
				DependsOn: []string{"mix-audio"},
				Timeout:   "120s",
			},
			"add-subtitles": {
				Handler: "video.subtitles",
				Params: map[string]interface{}{
					"video_path":    "${lip-sync.output_path}",
					"subtitle_path": "${generate-subtitles.output_path}",
				},
				DependsOn: []string{"lip-sync", "generate-subtitles"},
				Timeout:   "60s",
			},
			"encode-output": {
				Handler: "video.encode",
				Params: map[string]interface{}{
					"video_path": "${add-subtitles.output_path}",
					"resolution": resolution,
					"codec":      "h264",
					"format":     "mp4",
				},
				DependsOn: []string{"add-subtitles"},
				Timeout:   "300s",
			},
			"check-quality": {
				Handler:   "quality.video_check",
				Params:    map[string]interface{}{"video_path": "${encode-output.output_path}"},
				DependsOn: []string{"encode-output"},
				Timeout:   "30s",
			},
			"check-face": {
				Handler: "quality.face_check",
				Params: map[string]interface{}{
					"video_path":      "${encode-output.output_path}",
					"face_image_path": "${download-face-image.output_path}",
				},
				DependsOn: []string{"encode-output", "download-face-image"},
				Timeout:   "30s",
			},
			"upload": {
				Handler:   "media.upload",
				Params:    map[string]interface{}{"file_path": "${encode-output.output_path}"},
				DependsOn: []string{"check-quality", "check-face"},
				Timeout:   "120s",
			},
		},
	}

	data, err := yaml.Marshal(dag)
	if err != nil {
		// Should never happen with well-formed structs.
		return fmt.Sprintf("# yaml.Marshal error: %v", err)
	}
	return string(data)
}

