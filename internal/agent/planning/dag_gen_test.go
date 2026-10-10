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

	prompt := planner.buildPlanPrompt(&Requirement{Description: "x"}, "")

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

	prompt := planner.buildPlanPrompt(&Requirement{Description: "x"}, "[L3/error] task \"x\" uses unknown handler \"nope\"")

	assert.Contains(t, prompt, "unknown handler")
	assert.Contains(t, prompt, "修正", "the retry must ask for a fix")
}

// The quality gate is taught only when the requirement declared one. Emitting it
// always would add a step to every plan; emitting it never would mean a declared
// acceptance is written down and then never checked.
func TestPlannerTeachesTheGateOnlyWhenDeclared(t *testing.T) {
	newPrompt := func(a Acceptance) string {
		planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{})
		return planner.buildPlanPrompt(&Requirement{Description: "x", Acceptance: a}, "")
	}

	t.Run("nothing declared", func(t *testing.T) {
		prompt := newPrompt(Acceptance{Criteria: "输出可用"})
		assert.NotContains(t, prompt, "handler: judge",
			"a requirement with no bar must not get a gate step")
		assert.NotContains(t, prompt, "break_on")
	})

	t.Run("threshold declared", func(t *testing.T) {
		prompt := newPrompt(Acceptance{Criteria: "输出可用", Threshold: 0.8})
		assert.Contains(t, prompt, "judge", "a declared bar must be checked")
		assert.Contains(t, prompt, "0.8", "and the declared threshold is what the gate compares")
		assert.Contains(t, prompt, "break_on", "the loop is declared on the producing step")
		assert.Contains(t, prompt, "goto", "with a route back so a miss is retried")
		assert.Contains(t, prompt, "verdict", "the score lands in a named output the condition reads")
	})

	t.Run("artifacts declared", func(t *testing.T) {
		prompt := newPrompt(Acceptance{Criteria: "输出可用", Artifacts: []string{"report.md", "*.csv"}})
		assert.Contains(t, prompt, "report.md")
		assert.Contains(t, prompt, "*.csv", "every declared artifact is named")
		assert.Contains(t, prompt, "judge")
	})
}

// The prompt tells the planner NOT to choose the bar itself. A model that
// rewrote the threshold would put the decision back inside a prompt.
func TestGateHintForbidsInventingTheThreshold(t *testing.T) {
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{})
	prompt := planner.buildPlanPrompt(&Requirement{
		Description: "x",
		Acceptance:  Acceptance{Criteria: "c", Threshold: 0.75},
	}, "")

	assert.Contains(t, prompt, "不要自己改", "the declared threshold is not the model's to move")
}

// A nil requirement is a programming error, not a crash.
func TestGateHintHandlesNil(t *testing.T) {
	assert.Empty(t, gateHint(nil))
}

// The handlers the prompt teaches must be exactly the handlers the validator
// accepts.
//
// This is the guard that matters most in this file. The previous design told the
// model about one set of names and validated against another, so a generated DAG
// passed every layer and then had no worker to run it. Any handler added to the
// prompt without being added to the catalog shows up here — which is how the
// scorer was caught: the gate hint taught `handler: judge` while the catalog
// listed only the executor.
func TestEveryTaughtHandlerIsValidatable(t *testing.T) {
	planner := NewTaskPlanner(&scriptedLLM{}, NewHandlerCatalog(GeneratedHandlerSpecs()), stubProfile{})

	// A requirement that exercises the gate hint, so the prompt names every
	// handler it can ever name.
	prompt := planner.buildPlanPrompt(&Requirement{
		Description: "x",
		Acceptance:  Acceptance{Criteria: "c", Artifacts: []string{"a.txt"}, Threshold: 0.8},
	}, "")

	// Each handler the prompt tells the model it may use is looked for by name in
	// the catalog the validator will consult.
	for _, spec := range GeneratedHandlerSpecs() {
		assert.Contains(t, prompt, spec.Name,
			"the prompt must describe handler %q", spec.Name)

		validated := NewDAGValidator(NewHandlerCatalog(GeneratedHandlerSpecs()))
		result := validated.Validate("name: t\ntasks:\n  s:\n    handler: " + spec.Name + "\n    params:\n      action: run\n      task: 做点什么\n")
		for _, issue := range result.Issues {
			assert.NotContains(t, issue.Message, "unknown handler",
				"handler %q is taught by the prompt but rejected by the validator; "+
					"a generated plan using it would pass the model and fail L3", spec.Name)
		}
	}
}

// The catalog the planner is given is the one the prompt describes: assembly
// does not keep a second list.
func TestGeneratedHandlerSpecsCoverTheExecutorAndTheScorer(t *testing.T) {
	names := NewHandlerCatalog(GeneratedHandlerSpecs()).Names()

	assert.Contains(t, names, agentHandler)
	assert.Contains(t, names, judgeHandler)
	assert.Len(t, names, 2, "the list is what generated plans may use, and nothing else")
}

// The scorer's catalog entry must require what the generator writes, or every
// generated scorer step fails L4 for a param nobody can supply.
func TestScorerSpecRequiresOnlyWhatTheGeneratorWrites(t *testing.T) {
	spec := JudgeHandlerSpec()

	assert.Equal(t, judgeHandler, spec.Name)
	assert.Contains(t, spec.Required, "action",
		"the worker adapter needs an action, and the generator writes it")
	assert.NotContains(t, spec.Required, paramTask,
		"a scorer has nothing to achieve; requiring a task would fail every plan")
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
