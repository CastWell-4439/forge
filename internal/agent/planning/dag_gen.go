package planning

import (
	"context"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/coordinator"
)

const (
	// maxRetries is the maximum number of LLM retry attempts for DAG generation.
	maxRetries = 3
)

// DAGGenerator orchestrates DAG generation using a three-strategy approach:
// 1. Template matching (fast, stable)
// 2. LLM generation + validation (flexible, with retry)
// 3. Fallback template (always succeeds, may lose detail)
// From agent-tech-spec section 5.1.
type DAGGenerator struct {
	planner   *TaskPlanner
	validator *DAGValidator
	registry  *workers.ToolRegistry
	llmClient core.LLMClient
}

// NewDAGGenerator creates a new DAGGenerator.
func NewDAGGenerator(llm core.LLMClient, registry *workers.ToolRegistry) *DAGGenerator {
	return &DAGGenerator{
		planner:   NewTaskPlanner(llm, registry),
		validator: NewDAGValidator(registry),
		registry:  registry,
		llmClient: llm,
	}
}

// GenerateResult holds the result of DAG generation.
type GenerateResult struct {
	DAG      *coordinator.DAG
	YAML     string
	Strategy string // "template", "llm", "fallback"
	Retries  int    // number of LLM retries used
}

// Generate produces a validated DAG from a VideoRequirement.
// It tries three strategies in order: template, LLM+validate, fallback.
func (g *DAGGenerator) Generate(ctx context.Context, req *core.VideoRequirement) (*GenerateResult, error) {
	// Strategy 1: Template matching — check if any template fits.
	for _, tmpl := range g.planner.templates {
		if tmpl.Match(req) {
			yamlStr := tmpl.Build(req)
			result := g.validator.Validate(yamlStr)
			if result.Valid && result.DAG != nil {
				return &GenerateResult{
					DAG:      result.DAG,
					YAML:     yamlStr,
					Strategy: "template",
				}, nil
			}
			// Template produced invalid DAG — fall through to LLM.
			break
		}
	}

	// Strategy 2: LLM dynamic generation with validation and retry.
	var lastErrors string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		yamlStr, err := g.generateWithLLM(ctx, req, lastErrors)
		if err != nil {
			return nil, fmt.Errorf("generate DAG (attempt %d): %w", attempt, err)
		}

		result := g.validator.Validate(yamlStr)
		if result.Valid && result.DAG != nil {
			return &GenerateResult{
				DAG:      result.DAG,
				YAML:     yamlStr,
				Strategy: "llm",
				Retries:  attempt,
			}, nil
		}

		// Feed errors back for next retry.
		lastErrors = result.ErrorSummary()
	}

	// Strategy 3: Fallback — use a minimal template that always works.
	yamlStr := g.buildFallbackDAG(req)
	dag, err := coordinator.ParseDAG([]byte(yamlStr))
	if err != nil {
		return nil, fmt.Errorf("generate DAG: fallback parse failed: %w", err)
	}

	return &GenerateResult{
		DAG:      dag,
		YAML:     yamlStr,
		Strategy: "fallback",
		Retries:  maxRetries,
	}, nil
}

// generateWithLLM calls the LLM to generate a DAG, optionally including
// error feedback from a previous attempt.
func (g *DAGGenerator) generateWithLLM(ctx context.Context, req *core.VideoRequirement, previousErrors string) (string, error) {
	if previousErrors == "" {
		return g.planner.planWithLLM(ctx, req)
	}

	// Retry with error feedback — from agent-tech-spec 3.3.1.
	selectedTools := g.planner.selectTools(req)
	toolsPrompt := g.registry.FormatForPrompt()

	reqPrompt := req.ToPromptString()

	systemPrompt := fmt.Sprintf(`你是一个视频处理 DAG 编排专家。根据用户的视频制作需求，生成 Forge DAG YAML。

⚠️ 你上一次生成的 DAG 有以下问题：
%s

请修复以上问题，重新生成正确的 DAG YAML。

规则：
1. 每个 task 必须指定 handler 和 params
2. depends_on 必须引用已存在的 task 名称
3. 没有依赖的 task 将并行执行
4. DAG 必须包含 name 字段
5. 每个 task 的 handler 必须是以下可用 handler 之一

可用 handler 列表：
%s

推荐 handler：%s

只输出纯 YAML，不要包含 markdown 代码块或任何解释文字。`,
		previousErrors, toolsPrompt,
		fmt.Sprintf("%v", selectedTools))

	messages := []core.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: reqPrompt},
	}

	raw, err := g.llmClient.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	return g.planner.fixDAG(raw), nil
}

// buildFallbackDAG creates a minimal but valid DAG that covers the basic
// operations. This always succeeds but may lose some detail from the
// original requirement.
func (g *DAGGenerator) buildFallbackDAG(req *core.VideoRequirement) string {
	// Build a minimal download -> encode -> upload pipeline.
	sourceURL := "input"
	if len(req.SourceVideos) > 0 {
		sourceURL = req.SourceVideos[0].URL
	}
	resolution := req.Resolution
	if resolution == "" {
		resolution = "1080p"
	}

	return fmt.Sprintf(`name: fallback-pipeline
tasks:
  download:
    handler: media.download
    params:
      url: "%s"
    timeout: 60s
  encode:
    handler: video.encode
    params:
      resolution: "%s"
      codec: h264
      format: mp4
    depends_on:
      - download
    timeout: 300s
  upload:
    handler: media.upload
    params: {}
    depends_on:
      - encode
    timeout: 120s`, sourceURL, resolution)
}
