package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// DAGTemplate is a pre-built DAG shape, supplied by a domain.
//
// A template earns its place only if it is both common and fixed — the shape
// recurs, and its steps do not depend on the requirement's details. A template
// that has to inspect domain fields to decide its own shape is not a template;
// it is a planner written in Go, and it will be wrong the first time reality
// differs slightly.
type DAGTemplate struct {
	// Name is the template identifier.
	Name string
	// Description explains what shape this template builds.
	Description string
	// Match reports whether this template fits the requirement. A template that
	// inspects req.Fields is doing so by agreement with its own domain.
	Match func(req *Requirement) bool
	// Build renders the DAG YAML.
	Build func(req *Requirement) (string, error)
}

// TaskPlanner converts a structured requirement into workflow DAG YAML.
//
// Strategy A tries the domain's templates. Strategy B asks a model, giving it
// the handler catalog and the requirement — and nothing about tools, because the
// generated steps dispatch to the executor and name their goal instead.
type TaskPlanner struct {
	llmClient core.LLMClient
	catalog   *HandlerCatalog
	profile   DomainProfile
	templates []DAGTemplate
}

// NewTaskPlanner creates a planner for a domain.
//
// A nil profile means the generic one; a nil catalog means no handlers are
// known, which is a valid state for a caller that only wants templates.
func NewTaskPlanner(llm core.LLMClient, catalog *HandlerCatalog, profile DomainProfile) *TaskPlanner {
	if profile == nil {
		profile = GenericProfile{}
	}
	return &TaskPlanner{
		llmClient: llm,
		catalog:   catalog,
		profile:   profile,
		templates: profile.Templates(),
	}
}

// Plan generates DAG YAML for the requirement: templates first, then the model.
func (p *TaskPlanner) Plan(ctx context.Context, req *Requirement) (string, error) {
	for _, tmpl := range p.templates {
		if tmpl.Match == nil || tmpl.Match(req) {
			yamlStr, err := tmpl.Build(req)
			if err != nil {
				return "", fmt.Errorf("plan: template %s: %w", tmpl.Name, err)
			}
			return yamlStr, nil
		}
	}
	return p.planWithLLM(ctx, req, "")
}

// Templates returns the active templates, for the generator to try without
// re-asking the planner which one matches.
func (p *TaskPlanner) Templates() []DAGTemplate { return p.templates }

// planWithLLM asks the model for a DAG. previousErrors, when set, is fed back so
// a retry is a correction rather than a repetition.
func (p *TaskPlanner) planWithLLM(ctx context.Context, req *Requirement, previousErrors string) (string, error) {
	systemPrompt := p.buildPlanPrompt(req, previousErrors)

	reqJSON, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", fmt.Errorf("plan with LLM: marshal requirement: %w", err)
	}

	messages := []core.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(reqJSON)},
	}

	raw, err := p.llmClient.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("plan with LLM: LLM call failed: %w", err)
	}

	return fixDAG(raw), nil
}

// buildPlanPrompt assembles the planning instruction.
//
// It teaches the one shape that matters: every step is a self-contained task
// dispatched to the executor, saying what to achieve and what done means. The
// model is NOT asked to pick tools — it cannot see the workspace or the data,
// and a tool chosen blind is a guess the executor has to live with. The executor
// picks, at a point where it can actually tell which tool fits.
func (p *TaskPlanner) buildPlanPrompt(req *Requirement, previousErrors string) string {
	var b strings.Builder

	b.WriteString("你是一个工作流编排专家。根据下面的结构化需求，生成一份 Forge 工作流 DAG YAML。\n\n")

	if previousErrors != "" {
		b.WriteString("⚠️ 你上一次生成的 DAG 有以下问题，请修正后重新生成：\n")
		b.WriteString(previousErrors)
		b.WriteString("\n\n")
	}

	b.WriteString("规则：\n1. 每个 task 的 handler 必须是下列可用 handler 之一。\n\n")
	fmt.Fprintf(&b, "可用 handler：\n%s\n\n", p.catalog.FormatForPrompt())

	b.WriteString(`2. 每一步都交给 agent 执行，用 params 说明这一步要做什么，而不是指定用什么工具：
     params.task       要达成的结果，一句话，具体到不需要再问
     params.acceptance 这一步的验收标准（可选），怎么算做完
   工具的选择由执行者在运行时决定——它能看到工作区和实际数据，比在规划阶段盲选更可靠。
3. depends_on 只能引用已存在的 task 名称；没有依赖的 task 会并行执行。
4. DAG 必须有 name 字段。
5. 步骤要少而清楚。能一步做完的不要拆成三步。
6. task 说明里不要出现具体命令、工具名或代码——那是执行者的事。
7. 只输出纯 YAML，不要 markdown 代码块，不要任何解释文字。

`)

	// The quality gate, taught only when the requirement asked for one.
	//
	// Emitting it always would add a step to every plan, including ones that
	// declared no bar; emitting it never would mean an acceptance a requirement
	// did declare gets written down and then never checked. So it appears exactly
	// when the acceptance named artifacts or a score to reach.
	if gate := gateHint(req); gate != "" {
		b.WriteString(gate)
		b.WriteString("\n")
	}

	b.WriteString("示例：\n")
	b.WriteString(examplePlanYAML + "\n")

	if hints := strings.TrimSpace(p.profile.PlanHints()); hints != "" {
		b.WriteString("\n本领域补充说明：\n")
		b.WriteString(hints)
	}

	return b.String()
}

// gateHint describes the quality gate, when the requirement asked for one.
//
// The gate is a declared structure rather than a model decision: the scorer
// reports a score and never learns the bar, and the workflow compares the two.
// That split is why this hint gives a shape to reproduce instead of an
// instruction to "check the quality" — a step that decided for itself would put
// the verdict in its own output, where it cannot be reviewed or replayed.
//
// The loop is bounded, and exhausting it fails the run: a plan that keeps missing
// the bar must say so rather than quietly hand back the best attempt.
func gateHint(req *Requirement) string {
	if req == nil {
		return ""
	}
	artifacts := req.Acceptance.Artifacts
	threshold := req.Acceptance.Threshold
	if len(artifacts) == 0 && threshold <= 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("8. 需求声明了验收门槛，必须按下面的形状加一步质量评估，不要省：\n")
	b.WriteString("   - 产出步骤都声明 output，好让评估步骤能引用\n")
	b.WriteString("   - 加一个 handler: judge 的步骤，params.acceptance 原样带上需求里的验收标准\n")

	if len(artifacts) > 0 {
		fmt.Fprintf(&b, "     acceptance.artifacts 必须列出这些产物：%s\n", strings.Join(artifacts, "、"))
		b.WriteString("     评估会先检查它们齐不齐——缺了直接返回低分，这一步不调用模型\n")
	}

	if threshold > 0 {
		fmt.Fprintf(&b, "   - 产出步骤上声明 loop，break_on 用 results.verdict.score >= %g\n", threshold)
		fmt.Fprintf(&b, "     %g 是需求声明的门槛，不要自己改\n", threshold)
		b.WriteString("   - 评估步骤的 on_result.success 写成 {action: goto, target: <第一个产出步骤>}\n")
		b.WriteString("     回跳是否真的发生由 break_on 决定：达标就结束循环，未达标就重做这一段\n")
	}

	b.WriteString("   - 评估步骤的 output 命名为 verdict，便于 break_on 引用\n")
	return b.String()
}

// examplePlanYAML is a worked example in the planning prompt.
//
// A concrete shape is worth more than another rule: the rules say steps go to the
// executor, and this shows what that looks like. It is domain-free on purpose —
// an example drawn from this project's own work would teach the model to imitate
// it rather than to plan.
const examplePlanYAML = `name: example-plan
tasks:
  gather:
    handler: agent
    params:
      task: 收集完成该需求所需的全部输入，并记录它们的来源
      acceptance: 每项输入都指出了具体来源；缺什么、为什么缺，都写清楚了
    timeout: 10m

  produce:
    handler: agent
    params:
      task: 依据已收集的输入产出目标成果
      acceptance: 成果满足需求里声明的验收标准；不满足的部分要说明差在哪里
    depends_on:
      - gather
    timeout: 30m`

// fixDAG strips markdown fences from model output.
//
// Models wrap YAML despite being told not to, and a fence is a formatting habit
// rather than a mistake worth a retry: stripping it costs nothing, while
// rejecting it would spend a whole round trip on punctuation.
func fixDAG(raw string) string {
	s := strings.TrimSpace(raw)

	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	return s
}
