package test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/planning"
	"github.com/castwell/forge/internal/agent/session"
)

// scriptedLLM answers by inspecting the system prompt, so one mock can serve the
// parser and the planner without the caller tracking call order.
type scriptedLLM struct {
	requirementReply string
	planReply        string
}

func (m *scriptedLLM) Chat(_ context.Context, messages []core.Message) (string, error) {
	for _, msg := range messages {
		if msg.Role != "system" {
			continue
		}
		if strings.Contains(msg.Content, "需求分析师") {
			return m.requirementReply, nil
		}
		if strings.Contains(msg.Content, "工作流编排专家") {
			return m.planReply, nil
		}
	}
	return m.requirementReply, nil
}

func (m *scriptedLLM) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
	resp, err := m.Chat(ctx, messages)
	return core.ChatResult{Content: resp}, err
}

// catalog builds the workflow handler set an end-to-end run validates against.
// It mirrors what the deployment registers, which is the point: validation and
// dispatch must agree, and this is the seam where they can drift.
func catalog() *planning.HandlerCatalog {
	return planning.NewHandlerCatalog([]planning.HandlerSpec{
		planning.AgentHandlerSpec(),
	})
}

// TestAgentE2ELLMFlow walks the Plan half end to end: text → requirement → DAG →
// four-layer validation, with a domain-neutral vocabulary throughout.
func TestAgentE2ELLMFlow(t *testing.T) {
	llm := &scriptedLLM{
		requirementReply: `{
			"description": "把过长的录屏剪成 5~15 秒并导出",
			"fields": {"target_duration": "5-15s", "source": "recording.mp4"},
			"acceptance": {
				"criteria": "导出文件时长在 5~15 秒之间，画面与源一致",
				"checks": ["时长在范围内", "画面未被裁错位置"]
			}
		}`,
		planReply: `name: trim-recording
tasks:
  inspect:
    handler: agent
    params:
      action: run
      task: 查看录屏，确定要保留的片段的起止位置
      acceptance: 明确说出起止时间点及依据
    timeout: 10m
  trim:
    handler: agent
    params:
      action: run
      task: 按确定的时间点裁剪并导出
      acceptance: 导出文件时长在 5~15 秒之间
    depends_on: [inspect]
    timeout: 10m`,
	}

	parser := planning.NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "把这段录屏剪短一点")
	require.NoError(t, err)

	assert.Equal(t, "把过长的录屏剪成 5~15 秒并导出", req.Description)
	assert.Equal(t, "5-15s", req.Fields["target_duration"])
	assert.Len(t, req.Acceptance.Checks, 2)

	gen := planning.NewDAGGenerator(llm, catalog(), nil)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, 0, result.Retries)
	assert.Equal(t, "trim-recording", result.DAG.Name)
	require.Len(t, result.DAG.Tasks, 2)

	// Every step dispatches to the executor and says what to achieve. No step
	// names a tool: that decision belongs to whoever runs the step.
	for name, task := range result.DAG.Tasks {
		assert.Equal(t, "agent", task.Handler, "task %q must dispatch to the executor", name)
		assert.NotEmpty(t, task.Params["task"], "task %q must say what to achieve", name)
		assert.Equal(t, "run", task.Params["action"])
	}

	assert.Equal(t, []string{"inspect"}, result.DAG.Tasks["trim"].DependsOn)

	// The whole point of the pass: this DAG is dispatchable.
	validator := planning.NewDAGValidator(catalog())
	valResult := validator.Validate(result.YAML)
	assert.True(t, valResult.Valid, "validation failed: %v", valResult.Issues)

	sorted, err := result.DAG.TopologicalSort()
	require.NoError(t, err)
	assert.Len(t, sorted, 2)
	assert.Equal(t, "inspect", sorted[0], "the dependency must come first")
}

// A DAG naming a tool as a handler used to pass every layer and then have no
// worker to run it. This is the end-to-end statement that it no longer does.
func TestAgentE2EToolNameAsHandlerIsRejected(t *testing.T) {
	llm := &scriptedLLM{
		requirementReply: `{"description": "取一份网页内容"}`,
		planReply: `name: tool-name-as-handler
tasks:
  fetch:
    handler: web.fetch
    params:
      url: "https://example.com"
`,
	}

	parser := planning.NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "取一份网页内容")
	require.NoError(t, err)

	gen := planning.NewDAGGenerator(llm, catalog(), nil)
	result, err := gen.Generate(context.Background(), req)
	// The model keeps naming a tool, so the generator exhausts its retries and
	// ends on the fallback. Either way it never hands back an unrunnable DAG.
	require.NoError(t, err)
	assert.Equal(t, "fallback", result.Strategy,
		"a plan that cannot be dispatched must not be returned as if it could")

	for name, task := range result.DAG.Tasks {
		assert.NotEqual(t, "web.fetch", task.Handler, "task %q must not name a tool", name)
	}
}

// TestAgentE2EDomainProfileFlow shows the domain seam working: a profile supplies
// the parse instruction and the plan hints, and nothing about that domain reaches
// the engine.
func TestAgentE2EDomainProfileFlow(t *testing.T) {
	profile := supportProfile{}
	llm := &scriptedLLM{
		requirementReply: `{
			"description": "处理 42 号工单",
			"fields": {"ticket": "42", "priority": "high"}
		}`,
		planReply: `name: ticket-42
tasks:
  handle:
    handler: agent
    params:
      action: run
      task: 处理 42 号工单并留下记录
    timeout: 20m`,
	}

	parser := planning.NewRequirementParser(llm, profile)
	require.Equal(t, "support", parser.ProfileName())

	req, err := parser.Parse(context.Background(), "处理一下 42 号工单")
	require.NoError(t, err)
	assert.Equal(t, "42", req.Fields["ticket"])

	gen := planning.NewDAGGenerator(llm, catalog(), profile)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, "ticket-42", result.DAG.Name)
	assert.Equal(t, "agent", result.DAG.Tasks["handle"].Handler)
}

// supportProfile is a domain the engine knows nothing about. It exists to prove
// the seam: the engine's own files mention no ticket, no priority, and no
// support process — they come from here.
type supportProfile struct{}

func (supportProfile) Name() string { return "support" }

func (supportProfile) ParseSystemPrompt() string {
	return `你是一个需求分析师，负责把工单处理请求整理成结构化需求。
输出 JSON：description / fields{ticket, priority} / acceptance{criteria, checks}。`
}

func (supportProfile) PlanHints() string {
	return "工单处理的步骤通常是：先确认工单内容，再执行变更，最后留记录。"
}

func (supportProfile) Templates() []planning.DAGTemplate { return nil }

// TestAgentE2ESessionFlow tests the session state machine through a full flow.
func TestAgentE2ESessionFlow(t *testing.T) {
	sess := session.NewSession()
	store := session.NewInMemorySessionStore()

	require.NoError(t, store.Save(sess))

	require.NoError(t, sess.Transition(session.StateParsing))
	sess.AddMessage(core.Message{Role: "user", Content: "帮我整理一份周报"})

	mock := &scriptedLLM{
		requirementReply: `{"description": "整理一份周报", "acceptance": {"criteria": "覆盖本周全部进展"}}`,
		planReply: `name: weekly
tasks:
  write:
    handler: agent
    params:
      action: run
      task: 整理本周进展成一份周报
    timeout: 20m`,
	}

	parser := planning.NewRequirementParser(mock, nil)
	req, err := parser.Parse(context.Background(), "帮我整理一份周报")
	require.NoError(t, err)

	// The session carries the domain requirement as an opaque value: it is a
	// holder, not an interpreter.
	sess.SetRequirement(req)
	require.NotNil(t, sess.Requirement)

	require.NoError(t, sess.Transition(session.StatePlanning))

	gen := planning.NewDAGGenerator(mock, catalog(), nil)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "weekly", result.DAG.Name)

	require.NoError(t, sess.Transition(session.StateExecuting))
	sess.SetWorkflowID("wf-test-123")

	require.NoError(t, sess.Transition(session.StateChecking))
	require.NoError(t, sess.Transition(session.StateCompleted))

	assert.Equal(t, session.StateCompleted, sess.GetState())
	assert.Equal(t, "wf-test-123", sess.WorkflowID)

	retrieved, err := store.Get(sess.ID)
	require.NoError(t, err)
	assert.Equal(t, session.StateCompleted, retrieved.GetState())
}

// TestAgentE2EFallbackAlwaysProducesARunnableDAG: when the model cannot plan,
// the requirement still runs — as one executor step — rather than failing to
// start. The yardstick is dispatchability, not elegance.
func TestAgentE2EFallbackAlwaysProducesARunnableDAG(t *testing.T) {
	mock := &scriptedLLM{
		requirementReply: `{
			"description": "把 \"登录失败: 超时\" 这个报错查清楚",
			"acceptance": {"criteria": "说明根因并给出可验证的结论"}
		}`,
		planReply: "这不是 YAML",
	}

	parser := planning.NewRequirementParser(mock, nil)
	req, err := parser.Parse(context.Background(), "查一下这个报错")
	require.NoError(t, err)

	gen := planning.NewDAGGenerator(mock, catalog(), nil)
	result, err := gen.Generate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "fallback", result.Strategy)
	require.Len(t, result.DAG.Tasks, 1)

	task := result.DAG.Tasks["work"]
	assert.Equal(t, "agent", task.Handler)

	// The requirement survives intact, including the quoting that would break a
	// formatted YAML string.
	assert.Equal(t, req.Description, task.Params["task"])
	assert.Contains(t, task.Params["acceptance"], "说明根因")

	// And it validates, which is what makes it runnable.
	valResult := planning.NewDAGValidator(catalog()).Validate(result.YAML)
	assert.True(t, valResult.Valid, "the fallback must always validate: %v", valResult.Issues)
}
