// Package planning implements requirement parsing, task planning, and DAG
// generation for the Agent layer.
package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/domain"
)

// dagYAML is the top-level struct for generating DAG YAML via yaml.Marshal.
// This replaces the unsafe fmt.Sprintf approach (#6).
type dagYAML struct {
	Name  string              `yaml:"name"`
	Tasks map[string]taskYAML `yaml:"tasks"`
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

// DAGTemplate is a predefined DAG template for a recurring task shape
// scenarios. Strategy A from agent-tech-spec 3.3.
type DAGTemplate struct {
	// Name is the template identifier.
	Name string
	// Description explains what this template is for.
	Description string
	// Match returns true if the given requirement fits this template.
	Match func(req *domain.VideoRequirement) bool
	// Build generates a YAML DAG string from the requirement.
	Build func(req *domain.VideoRequirement) string
}

// TaskPlanner converts a structured requirement into Forge DAG YAML.
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
func (p *TaskPlanner) Plan(ctx context.Context, req *domain.VideoRequirement) (string, error) {
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
func (p *TaskPlanner) planWithLLM(ctx context.Context, req *domain.VideoRequirement) (string, error) {
	selectedTools := p.selectTools(req)
	toolsPrompt := p.registry.FormatForPrompt()

	reqJSON, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", fmt.Errorf("plan with LLM: marshal requirement: %w", err)
	}

	systemPrompt := fmt.Sprintf(`你是一个 DAG 编排专家。根据下面的任务需求，生成 Forge DAG YAML。

规则：
1. 每个 task 必须指定 handler 和 params
2. depends_on 必须引用已存在的 task 名称
3. 没有依赖的 task 将并行执行
4. DAG 必须包含 name 字段
5. 每个 task 的 handler 必须是以下可用 handler 之一

可用 handler 列表：
%s

推荐使用的 handler（根据需求分析）：
%s

只输出纯 YAML，不要包含 markdown 代码块或任何解释文字。`, toolsPrompt, strings.Join(selectedTools, ", "))

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
//
// The mapping is deliberately generic - sources are fetched, material is
// inspected, every transformation is a scripted step, the result is written out
// and checked. Which requirement block triggers which recommendation still keys
// off VideoRequirement until that type goes domain-neutral (A.11).
func (p *TaskPlanner) selectTools(req *domain.VideoRequirement) []string {
	var selected []string
	seen := make(map[string]bool)
	add := func(names ...string) {
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				selected = append(selected, name)
			}
		}
	}

	// Source material handling: fetch it.
	if len(req.SourceVideos) > 0 || len(req.SourceImages) > 0 || len(req.SourceAudios) > 0 {
		add("web.fetch")
	}

	// Video sources get inspected before anything runs against them.
	if len(req.SourceVideos) > 0 {
		add("file.read")
	}

	// Every transformation block - face work, lip sync, narration, script,
	// soundtrack, subtitles - is one scripted step in the neutral vocabulary.
	if req.FaceSwap != nil || req.LipSync != nil || req.TTS != nil ||
		req.Script != nil || req.BGM != nil || req.Subtitles != nil {
		add("code.execute")
	}

	// The result always gets published.
	add("code.execute", "file.write")

	// Quality levels are checked by running a checker, not by a dedicated tool.
	if req.QualityLevel == domain.QualityStandard || req.QualityLevel == domain.QualityPremium {
		add("shell.run")
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
		SourcePipelineTemplate(),
	}
}

// SourcePipelineTemplate returns the DAG template for a requirement that
// specifies all of its processing blocks: fetch the sources, run the scripted
// transform steps, publish the result.
//
// The match still keys off the requirement's feature blocks because the
// requirement type is VideoRequirement until the domain profile moves out
// (A.11); the built YAML itself uses only domain-neutral handlers.
func SourcePipelineTemplate() DAGTemplate {
	return DAGTemplate{
		Name:        "source_pipeline",
		Description: "Fetch sources, transform them with scripted steps, publish the result",
		Match: func(req *domain.VideoRequirement) bool {
			return req.FaceSwap != nil &&
				req.TTS != nil &&
				req.BGM != nil &&
				req.Subtitles != nil &&
				len(req.SourceVideos) > 0
		},
		Build: buildSourcePipeline,
	}
}

// buildSourcePipeline generates DAG YAML using struct + yaml.Marshal (#6 fix).
// Requirement values (narration text, soundtrack style, resolution, ...) travel
// as data inside the scripted steps, so the YAML stays readable from the
// requirement alone.
func buildSourcePipeline(req *domain.VideoRequirement) string {
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
	subLang := ""
	if req.Subtitles != nil {
		subLang = req.Subtitles.Language
	}

	dag := dagYAML{
		Name: "source-pipeline",
		Tasks: map[string]taskYAML{
			"fetch-source": {
				Handler: "web.fetch",
				Params:  map[string]interface{}{"url": sourceURL},
				Timeout: "60s",
			},
			"fetch-asset": {
				Handler: "web.fetch",
				Params:  map[string]interface{}{"url": faceURL},
				Timeout: "60s",
			},
			"prepare": {
				Handler:   "code.execute",
				Params:    map[string]interface{}{"language": "go", "code": fmt.Sprintf("prepare(source=%q, asset=%q)", sourceURL, faceURL)},
				DependsOn: []string{"fetch-source", "fetch-asset"},
				Timeout:   "60s",
			},
			"transform": {
				Handler:   "code.execute",
				Params:    map[string]interface{}{"language": "go", "code": fmt.Sprintf("transform(source=%q, resolution=%q)", sourceURL, resolution)},
				DependsOn: []string{"prepare"},
				Timeout:   "600s",
				Retry:     &retryYAML{MaxAttempts: 2, Backoff: "exponential", InitialInterval: "10s"},
			},
			"narrate": {
				Handler: "code.execute",
				Params: map[string]interface{}{
					"language": "go",
					"code":     fmt.Sprintf("narrate(text=%q, voice=%q, language=%q)", ttsText, ttsVoice, ttsLang),
				},
				Timeout: "60s",
			},
			"soundtrack": {
				Handler: "code.execute",
				Params: map[string]interface{}{
					"language": "go",
					"code":     fmt.Sprintf("soundtrack(style=%q, volume=%.2f)", bgmStyle, bgmVolume),
				},
				Timeout: "30s",
			},
			"combine": {
				Handler:   "code.execute",
				Params:    map[string]interface{}{"language": "go", "code": "combine(narrate.output, soundtrack.output)"},
				DependsOn: []string{"narrate", "soundtrack"},
				Timeout:   "60s",
			},
			"annotate": {
				Handler:   "code.execute",
				Params:    map[string]interface{}{"language": "go", "code": fmt.Sprintf("annotate(combine.output, language=%q)", subLang)},
				DependsOn: []string{"combine"},
				Timeout:   "120s",
			},
			"publish": {
				Handler:   "file.write",
				Params:    map[string]interface{}{"path": "output/result.txt", "content": "${transform.stdout}"},
				DependsOn: []string{"transform", "annotate"},
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
