// Package planning implements requirement parsing, task planning, and DAG
// generation for the Agent layer.
package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

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
	registry  *workers.ToolRegistry
	templates []DAGTemplate
}

// NewTaskPlanner creates a new TaskPlanner.
func NewTaskPlanner(llm core.LLMClient, registry *workers.ToolRegistry) *TaskPlanner {
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
		Build: func(req *core.VideoRequirement) string {
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
			bgmVolume := "0.3"
			if req.BGM != nil {
				if req.BGM.Style != "" {
					bgmStyle = req.BGM.Style
				}
				if req.BGM.Volume > 0 {
					bgmVolume = fmt.Sprintf("%.1f", req.BGM.Volume)
				}
			}
			resolution := req.Resolution
			if resolution == "" {
				resolution = "1080p"
			}

			return fmt.Sprintf(`name: face-swap-with-tts
tasks:
  download-source-video:
    handler: media.download
    params:
      url: "%s"
    timeout: 60s
  download-face-image:
    handler: media.download
    params:
      url: "%s"
    timeout: 60s
  probe-video:
    handler: video.probe
    params:
      video_path: "${download-source-video.output_path}"
    depends_on:
      - download-source-video
    timeout: 10s
  preprocess-video:
    handler: video.preprocess
    params:
      video_path: "${probe-video.video_path}"
    depends_on:
      - probe-video
    timeout: 300s
  face-swap:
    handler: ai.face_swap
    params:
      video_path: "${preprocess-video.output_path}"
      face_image_path: "${download-face-image.output_path}"
      face_index: 0
    depends_on:
      - preprocess-video
      - download-face-image
    timeout: 600s
    retry:
      max_attempts: 2
      backoff: exponential
      initial_interval: 10s
  generate-tts:
    handler: ai.tts
    params:
      text: "%s"
      voice: "%s"
      language: "%s"
    timeout: 60s
  select-bgm:
    handler: audio.bgm_select
    params:
      style: "%s"
    timeout: 30s
  mix-audio:
    handler: audio.mix
    params:
      audio_paths: ["${generate-tts.output_path}", "${select-bgm.output_path}"]
      bgm_volume: %s
    depends_on:
      - generate-tts
      - select-bgm
    timeout: 60s
  lip-sync:
    handler: ai.lip_sync
    params:
      video_path: "${face-swap.output_path}"
      audio_path: "${mix-audio.output_path}"
    depends_on:
      - face-swap
      - mix-audio
    timeout: 300s
  generate-subtitles:
    handler: ai.subtitle_gen
    params:
      media_path: "${mix-audio.output_path}"
    depends_on:
      - mix-audio
    timeout: 120s
  add-subtitles:
    handler: video.subtitles
    params:
      video_path: "${lip-sync.output_path}"
      subtitle_path: "${generate-subtitles.output_path}"
    depends_on:
      - lip-sync
      - generate-subtitles
    timeout: 60s
  encode-output:
    handler: video.encode
    params:
      video_path: "${add-subtitles.output_path}"
      resolution: "%s"
      codec: h264
      format: mp4
    depends_on:
      - add-subtitles
    timeout: 300s
  check-quality:
    handler: quality.video_check
    params:
      video_path: "${encode-output.output_path}"
    depends_on:
      - encode-output
    timeout: 30s
  check-face:
    handler: quality.face_check
    params:
      video_path: "${encode-output.output_path}"
      face_image_path: "${download-face-image.output_path}"
    depends_on:
      - encode-output
      - download-face-image
    timeout: 30s
  upload:
    handler: media.upload
    params:
      file_path: "${encode-output.output_path}"
    depends_on:
      - check-quality
      - check-face
    timeout: 120s`, sourceURL, faceURL,
				ttsText, ttsVoice, ttsLang,
				bgmStyle, bgmVolume,
				resolution)
		},
	}
}
