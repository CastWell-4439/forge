package planworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/planning"
)

// stubLLM answers planning prompts with a fixed DAG.
type stubLLM struct {
	reply string
	err   error
}

func (s *stubLLM) Chat(_ context.Context, _ []core.Message) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

func (s *stubLLM) ChatWithUsage(ctx context.Context, m []core.Message) (core.ChatResult, error) {
	r, err := s.Chat(ctx, m)
	return core.ChatResult{Content: r}, err
}

// stubSubmitter reports a completed run so the worker's output shape can be
// checked without a coordinator.
type stubSubmitter struct {
	snapshot  *planning.WorkflowSnapshot
	submitted string
	submitErr error
}

func (s *stubSubmitter) Submit(_ context.Context, dagYAML string) (string, error) {
	if s.submitErr != nil {
		return "", s.submitErr
	}
	s.submitted = dagYAML
	return "wf-child", nil
}

func (s *stubSubmitter) Snapshot(_ context.Context, _ string) (*planning.WorkflowSnapshot, error) {
	if s.snapshot == nil {
		return &planning.WorkflowSnapshot{ID: "wf-child", Status: "RUNNING"}, nil
	}
	return s.snapshot, nil
}

const planReply = `name: planned
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`

func newWorkerWith(t *testing.T, sub *stubSubmitter, llm *stubLLM) *Worker {
	t.Helper()
	catalog := planning.NewHandlerCatalog([]planning.HandlerSpec{planning.AgentHandlerSpec()})
	runner := planning.NewRunner(planning.NewDAGGenerator(llm, catalog, nil), sub)
	return NewWorker(runner)
}

// A requirement goes in, a submitted plan and its outcome come out.
func TestPlannerWorkerRunsARequirement(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{
		ID:     "wf-child",
		Status: "COMPLETED",
		Tasks: []planning.TaskSnapshot{
			{Name: "work", Handler: "agent", Status: "COMPLETED", Output: json.RawMessage(`{"done":true,"note":"已记录出处"}`)},
		},
	}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"task":       "把某件事做完",
		"acceptance": "已记录出处",
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	assert.Equal(t, "wf-child", result["workflow_id"])
	assert.Equal(t, "llm", result["strategy"])
	assert.Equal(t, "COMPLETED", result["status"])
	assert.False(t, result["timed_out"].(bool))
	assert.Contains(t, result["plan"], "planned", "the plan travels back with the result")

	// The child workflow was actually submitted — that is what makes this the
	// Plan-and-Execute half rather than a planner with nowhere to send its plan.
	assert.Contains(t, sub.submitted, "handler: agent")

	// Task outputs are included, because they are the evidence an acceptance
	// check is matched against.
	tasks, ok := result["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	first := tasks[0].(map[string]any)
	assert.Equal(t, "work", first["name"])
	assert.Equal(t, "COMPLETED", first["status"])
}

// An acceptance written as one sentence is understood, so an author does not have
// to wrap it in a structure.
func TestPlannerWorkerAcceptsASentenceAcceptance(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks:  []planning.TaskSnapshot{{Name: "work", Output: json.RawMessage(`"结论有数据支撑"`)}},
	}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"task":       "x",
		"acceptance": "结论有数据支撑",
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	acc := result["acceptance"].(map[string]any)
	assert.Equal(t, "结论有数据支撑", acc["criteria"])
	assert.True(t, acc["passed"].(bool))
}

// An acceptance written as an object carries its checks through.
func TestPlannerWorkerAcceptsStructuredAcceptance(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks:  []planning.TaskSnapshot{{Name: "work", Output: json.RawMessage(`"记录了出处"`)}},
	}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"task": "x",
		"acceptance": map[string]any{
			"criteria": "报告完整",
			"checks":   []any{"记录了出处", "覆盖全部指标"},
		},
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	acc := result["acceptance"].(map[string]any)
	assert.Equal(t, "报告完整", acc["criteria"])

	checks := acc["checks"].([]any)
	require.Len(t, checks, 2)

	// The first is mentioned by the output, the second is not — and the unmentioned
	// one is what a reviewer should look at.
	first := checks[0].(map[string]any)
	second := checks[1].(map[string]any)
	assert.True(t, first["mentioned"].(bool))
	assert.False(t, second["mentioned"].(bool))

	unmentioned := acc["unmentioned"].([]any)
	assert.Equal(t, []any{"覆盖全部指标"}, unmentioned)
}

// `prompt` is accepted as an alias: authors reach for whichever word they think
// of first, and rejecting one of them teaches nothing.
func TestPlannerWorkerAcceptsPromptAlias(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{Status: "COMPLETED"}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	_, err := w.Execute(context.Background(), "run", map[string]any{"prompt": "做点什么"})
	require.NoError(t, err)
}

// A missing task is refused with a message that says what to supply.
func TestPlannerWorkerRequiresATask(t *testing.T) {
	sub := &stubSubmitter{}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	_, err := w.Execute(context.Background(), "run", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task")
	assert.Empty(t, sub.submitted, "nothing may be submitted without a requirement")
}

// An unknown action names what is supported.
func TestPlannerWorkerRejectsUnknownActions(t *testing.T) {
	w := newWorkerWith(t, &stubSubmitter{}, &stubLLM{reply: planReply})

	_, err := w.Execute(context.Background(), "plan", map[string]any{"task": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
	assert.Contains(t, err.Error(), "run", "the error must say what is supported")
}

// A nil runner is refused rather than panicking: a misconfigured deployment gets
// an error naming the problem.
func TestPlannerWorkerWithoutARunner(t *testing.T) {
	w := NewWorker(nil)

	_, err := w.Execute(context.Background(), "run", map[string]any{"task": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no runner")
}

// A planning failure is reported, not turned into an empty success.
func TestPlannerWorkerReportsPlanningFailure(t *testing.T) {
	sub := &stubSubmitter{}
	w := newWorkerWith(t, sub, &stubLLM{err: errors.New("model unreachable")})

	_, err := w.Execute(context.Background(), "run", map[string]any{"task": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model unreachable")
	assert.Empty(t, sub.submitted)
}

// A timeout is reported as a timeout, with the id, so the caller can wait longer
// or go and look — neither of which is possible if it is reported as a failure.
func TestPlannerWorkerReportsTimeout(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{ID: "wf-child", Status: "RUNNING"}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"task":    "x",
		"timeout": "1ms",
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	assert.True(t, result["timed_out"].(bool))
	assert.Equal(t, "wf-child", result["workflow_id"], "the id survives so the caller can look")
	assert.Equal(t, "RUNNING", result["status"])
}

// A child task that returned prose still said something: its text is handed back
// rather than dropped for not being JSON.
func TestPlannerWorkerKeepsNonJSONOutput(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{
		Status: "COMPLETED",
		Tasks: []planning.TaskSnapshot{
			{Name: "work", Status: "COMPLETED", Output: json.RawMessage(`this is prose, not json`)},
		},
	}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{"task": "x"})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	tasks := result["tasks"].([]any)
	first := tasks[0].(map[string]any)
	assert.Equal(t, "this is prose, not json", first["output"])
}

// A failing child task's error is carried back, so a caller sees why.
func TestPlannerWorkerCarriesTaskErrors(t *testing.T) {
	sub := &stubSubmitter{snapshot: &planning.WorkflowSnapshot{
		Status:   "FAILED",
		ErrorMsg: "a task blew up",
		Tasks: []planning.TaskSnapshot{
			{Name: "work", Status: "FAILED", ErrorMsg: "ran out of budget"},
		},
	}}
	w := newWorkerWith(t, sub, &stubLLM{reply: planReply})

	out, err := w.Execute(context.Background(), "run", map[string]any{"task": "x"})
	require.NoError(t, err, "a failed child run is a result, not a worker error")

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	assert.Equal(t, "FAILED", result["status"])
	assert.Equal(t, "a task blew up", result["error"])

	tasks := result["tasks"].([]any)
	first := tasks[0].(map[string]any)
	assert.Equal(t, "ran out of budget", first["error"])
}

// The timeout param accepts a duration string and a number of seconds, because a
// YAML author may write either.
func TestWaitTimeoutAcceptsBothShapes(t *testing.T) {
	assert.Equal(t, 90*1e9, float64(waitTimeout(map[string]any{"timeout": "90s"})))
	assert.Equal(t, 30*1e9, float64(waitTimeout(map[string]any{"timeout": float64(30)})))
	assert.Zero(t, waitTimeout(map[string]any{}), "no declaration means no bound")
	assert.Zero(t, waitTimeout(map[string]any{"timeout": "not a duration"}))
	assert.Zero(t, waitTimeout(map[string]any{"timeout": float64(-1)}))
}

// A whitespace-only task is treated as missing: it would produce a plan that says
// nothing, which is worse than an error.
func TestPlannerWorkerRejectsBlankTask(t *testing.T) {
	w := newWorkerWith(t, &stubSubmitter{}, &stubLLM{reply: planReply})

	_, err := w.Execute(context.Background(), "run", map[string]any{"task": "   "})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "task"))
}
