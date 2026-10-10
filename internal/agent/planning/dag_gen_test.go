package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// scriptedLLM replies with a fixed sequence, so the retry path can be driven
// deterministically: each call returns the next reply.
type scriptedLLM struct {
	replies          []string
	calls            int
	err              error
	lastSystemPrompt string
	lastUserPrompt   string
}

func (s *scriptedLLM) Chat(_ context.Context, messages []core.Message) (string, error) {
	for _, m := range messages {
		switch m.Role {
		case "system":
			s.lastSystemPrompt = m.Content
		case "user":
			s.lastUserPrompt = m.Content
		}
	}
	if s.err != nil {
		return "", s.err
	}
	if len(s.replies) == 0 {
		return "", errors.New("scriptedLLM: no replies configured")
	}
	// Repeat the last reply rather than indexing out of range: a test that
	// under-specifies its script should still fail on the validator's verdict,
	// not on a panic.
	idx := s.calls
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1
	}
	s.calls++
	return s.replies[idx], nil
}

// ChatWithUsage satisfies the rest of the client interface by delegating: the
// planning package reads only the text, and a second implementation of the same
// script would be a second thing to keep in step.
func (s *scriptedLLM) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
	content, err := s.Chat(ctx, messages)
	return core.ChatResult{Content: content}, err
}

// stubProfile is a domain with no opinions, for tests that exercise the engine.
type stubProfile struct {
	templates []DAGTemplate
	hints     string
}

func (s stubProfile) Name() string              { return "stub" }
func (s stubProfile) ParseSystemPrompt() string { return "parse it" }
func (s stubProfile) PlanHints() string         { return s.hints }
func (s stubProfile) Templates() []DAGTemplate  { return s.templates }

func TestGenerateUsesTemplateWhenItMatches(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "fixed-shape",
		Match: func(*Requirement) bool { return true },
		Build: func(*Requirement) (string, error) {
			return "name: from-template\ntasks:\n  work:\n    handler: agent\n    params:\n      action: run\n      task: 做点什么\n", nil
		},
	}
	llm := &scriptedLLM{}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)

	assert.Equal(t, "template", result.Strategy)
	assert.Equal(t, "from-template", result.DAG.Name)
	assert.Zero(t, llm.calls, "a matching template must not call the model")
}

// A template whose Match declines is skipped, not used.
func TestGenerateSkipsNonMatchingTemplate(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "never",
		Match: func(*Requirement) bool { return false },
		Build: func(*Requirement) (string, error) {
			t.Fatal("a non-matching template must not be built")
			return "", nil
		},
	}
	llm := &scriptedLLM{replies: []string{`
name: from-llm
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)
	assert.Equal(t, "llm", result.Strategy)
}

// When the model returns a valid DAG, it is used and the strategy says so.
func TestGenerateFallsThroughToTheModel(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`
name: from-llm
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
      acceptance: 做完了
`}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, "from-llm", result.DAG.Name)
	assert.Equal(t, 1, llm.calls)
	assert.Zero(t, result.Retries)
}

// An invalid DAG is sent back with its errors, and the retry is a correction:
// the second prompt carries what was wrong with the first.
//
// This is also the test for the vocabulary fix. The first reply names a tool as a
// handler — exactly what the old validator approved and the runtime could not
// dispatch — and the retry is what turns it into something runnable.
func TestGenerateRetriesWithErrorFeedback(t *testing.T) {
	llm := &scriptedLLM{replies: []string{
		"name: bad\ntasks:\n  work:\n    handler: web.fetch\n",
		"name: good\ntasks:\n  work:\n    handler: agent\n    params:\n      action: run\n      task: 做点什么\n",
	}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, "good", result.DAG.Name)
	assert.Equal(t, 1, result.Retries, "one retry was needed")
	assert.Equal(t, 2, llm.calls)
	assert.Contains(t, llm.lastSystemPrompt, "unknown handler",
		"the retry prompt must carry the previous failure, or it is a repetition rather than a correction")
}

// Exhausting the retries falls back to a single executor step, which always
// validates. The strategy is reported so a caller can see the breakdown was lost.
func TestGenerateFallsBackAfterExhaustingRetries(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"name: bad\ntasks:\n  work:\n    handler: web.fetch\n"}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	result, err := gen.Generate(context.Background(), &Requirement{
		Description: "把某件事做完",
		Acceptance:  Acceptance{Criteria: "做完且可核对"},
	})
	require.NoError(t, err)

	assert.Equal(t, "fallback", result.Strategy)
	require.NotNil(t, result.DAG)
	require.Len(t, result.DAG.Tasks, 1)

	task, ok := result.DAG.Tasks["work"]
	require.True(t, ok)
	assert.Equal(t, agentHandler, task.Handler)

	// The description and acceptance survive into the fallback: losing the step
	// breakdown is the cost, losing what was asked for would be a different and
	// much worse failure.
	raw, _ := task.Params[paramTask].(string)
	assert.Equal(t, "把某件事做完", raw)
	acc, _ := task.Params[paramAcceptance].(string)
	assert.Contains(t, acc, "做完且可核对")

	assert.NotZero(t, llm.calls, "the model was tried before falling back")
}

// A fallback whose description contains YAML-hostile characters still parses.
// Free text reaches this path by definition, and it is reached precisely when
// things have already gone wrong.
func TestFallbackSurvivesYAMLHostileText(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"not yaml at all"}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	desc := "修复 \"登录失败: 超时\" 的问题\n第二行也有内容"
	result, err := gen.Generate(context.Background(), &Requirement{Description: desc})
	require.NoError(t, err)

	assert.Equal(t, "fallback", result.Strategy)
	task := result.DAG.Tasks["work"]
	raw, _ := task.Params[paramTask].(string)
	assert.Equal(t, desc, raw, "the description survives verbatim through YAML quoting")
}

// A model that cannot be reached is an error, not a silent fallback: a provider
// outage and an unplannable requirement are different problems, and treating
// them alike would hide an outage behind a plan that quietly lost its steps.
func TestGenerateReportsLLMFailure(t *testing.T) {
	llm := &scriptedLLM{err: errors.New("provider down")}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	_, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider down")
}

// Markdown fences are stripped rather than rejected: a fence is a formatting
// habit, and spending a round trip on punctuation wastes the retry budget.
func TestGenerateStripsMarkdownFences(t *testing.T) {
	fenced := "```yaml\nname: fenced\ntasks:\n  work:\n    handler: agent\n    params:\n      action: run\n      task: 做点什么\n```"
	llm := &scriptedLLM{replies: []string{fenced}}
	gen := NewDAGGenerator(llm, testCatalog(), stubProfile{})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)

	assert.Equal(t, "llm", result.Strategy)
	assert.Equal(t, "fenced", result.DAG.Name)
	assert.Equal(t, 1, llm.calls, "a fence must not cost a retry")
}

// A nil catalog means no handler can be verified, so every generated DAG fails
// L3 and the generator ends on the fallback — which is still valid, because the
// fallback only uses the executor the generator knows by construction.
func TestGenerateWithNilCatalogStillProducesSomething(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`
name: unverifiable
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`}}
	gen := NewDAGGenerator(llm, nil, stubProfile{})

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)
	assert.Equal(t, "fallback", result.Strategy)
}

// A nil profile is the generic one rather than a crash.
func TestGenerateWithNilProfileUsesGeneric(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`
name: generic
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`}}
	gen := NewDAGGenerator(llm, testCatalog(), nil)

	result, err := gen.Generate(context.Background(), &Requirement{Description: "任何事"})
	require.NoError(t, err)
	assert.Equal(t, "generic", result.DAG.Name)
}

func TestPlannerPromptTeachesTheExecutorShape(t *testing.T) {
	profile := stubProfile{hints: "本领域的步骤通常是先取证再改。"}
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), profile)

	prompt := planner.buildPlanPrompt("")

	assert.Contains(t, prompt, "agent", "the executor must be named as the handler")
	assert.Contains(t, prompt, paramTask, "and the param that says what to do")
	assert.Contains(t, prompt, paramAcceptance, "and the one that says what done means")
	assert.Contains(t, prompt, "由执行者在运行时决定",
		"the model must be told NOT to pick tools — that is the design")
	assert.Contains(t, prompt, profile.hints, "domain hints are appended")
	assert.NotContains(t, prompt, "web.fetch", "no tool names belong in the planning prompt")
}

// A retry prompt carries the failures, so the second attempt is a correction.
func TestPlannerPromptCarriesPreviousErrors(t *testing.T) {
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{})

	prompt := planner.buildPlanPrompt("[L3/error] task \"x\" uses unknown handler \"nope\"")

	assert.Contains(t, prompt, "unknown handler")
	assert.Contains(t, prompt, "修正", "the retry must ask for a fix")
}

// The acceptance text passed to the executor carries both halves: the criterion
// says what outcome is wanted, the checks say how anyone will know.
func TestAcceptanceTextKeepsBothHalves(t *testing.T) {
	out := acceptanceText(Acceptance{
		Criteria: "输出在 5~15 秒之间",
		Checks:   []string{"时长在范围内", "内容与原文一致"},
	})

	assert.Contains(t, out, "5~15 秒")
	assert.Contains(t, out, "时长在范围内")
	assert.Contains(t, out, "内容与原文一致")
}

// A blank acceptance renders as blank, so the caller omits the param rather than
// passing an empty one.
func TestAcceptanceTextEmptyWhenNothingDeclared(t *testing.T) {
	assert.Empty(t, acceptanceText(Acceptance{}))
	assert.True(t, Acceptance{}.IsEmpty())
	assert.False(t, Acceptance{Criteria: "x"}.IsEmpty())
	assert.False(t, Acceptance{Checks: []string{"x"}}.IsEmpty())
}

// agentParams always writes the action, so the model is never asked for it and
// L4 always sees a task that has one.
func TestAgentParamsAlwaysCarryTheAction(t *testing.T) {
	params := agentParams("做某事", "")
	assert.Equal(t, agentAction, params["action"])
	assert.Equal(t, "做某事", params[paramTask])
	_, hasAcceptance := params[paramAcceptance]
	assert.False(t, hasAcceptance, "an undeclared acceptance must be omitted, not written empty")

	withAcceptance := agentParams("做某事", "做完了")
	assert.Equal(t, "做完了", withAcceptance[paramAcceptance])
}
