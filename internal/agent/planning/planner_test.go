package planning

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlannerUsesAMatchingTemplate(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "always",
		Match: func(*Requirement) bool { return true },
		Build: func(*Requirement) (string, error) { return "name: templated\n", nil },
	}
	llm := &scriptedLLM{}
	planner := NewTaskPlanner(llm, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	out, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)

	assert.Equal(t, "name: templated\n", out)
	assert.Zero(t, llm.calls, "a matching template must not call the model")
}

// A template that declines is skipped and the model is asked instead.
func TestPlannerSkipsDecliningTemplates(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "never",
		Match: func(*Requirement) bool { return false },
		Build: func(*Requirement) (string, error) {
			t.Fatal("must not build a template that declined")
			return "", nil
		},
	}
	llm := &scriptedLLM{replies: []string{"name: from-llm\n"}}
	planner := NewTaskPlanner(llm, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	out, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)

	assert.Contains(t, out, "from-llm")
	assert.Equal(t, 1, llm.calls)
}

// A template with no Match function is unconditional: a domain that supplies one
// shape for everything does not need to write a matcher that always says yes.
func TestPlannerTreatsNilMatchAsUnconditional(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "unconditional",
		Build: func(*Requirement) (string, error) { return "name: always\n", nil },
	}
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	out, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)
	assert.Contains(t, out, "always")
}

// A template that fails to build reports why, rather than being reported as a
// model failure.
func TestPlannerReportsTemplateBuildFailure(t *testing.T) {
	tmpl := DAGTemplate{
		Name:  "broken",
		Match: func(*Requirement) bool { return true },
		Build: func(*Requirement) (string, error) { return "", assert.AnError },
	}
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	_, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken", "the failing template must be named")
}

// The requirement reaches the model as JSON, including the opaque fields.
func TestPlannerSendsTheRequirementAsJSON(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"name: x\n"}}
	planner := NewTaskPlanner(llm, testCatalog(), stubProfile{})

	_, err := planner.Plan(context.Background(), &Requirement{
		Description: "整理报告",
		Fields:      map[string]any{"scope": "Q1"},
		Acceptance:  Acceptance{Criteria: "数字对齐"},
	})
	require.NoError(t, err)

	assert.Contains(t, llm.lastUserPrompt, "整理报告")
	assert.Contains(t, llm.lastUserPrompt, "Q1", "domain fields travel to the model untouched")
	assert.Contains(t, llm.lastUserPrompt, "数字对齐")
}

// The planner never asks the model to choose tools. That is the design: tool
// selection happens at execution time, where the executor can see the workspace
// and the data, and a blind choice made here is one the executor has to live with.
func TestPlannerDoesNotAskForToolSelection(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"name: x\n"}}
	planner := NewTaskPlanner(llm, testCatalog(), stubProfile{})

	_, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)

	prompt := llm.lastSystemPrompt
	assert.NotContains(t, prompt, "推荐使用的 handler")
	assert.NotContains(t, prompt, "选择工具")
	assert.Contains(t, prompt, "由执行者在运行时决定")
}

// Without templates and with a model that answers, the planner returns the model
// YAML with fences stripped.
func TestPlannerStripsFencesFromModelOutput(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"```yaml\nname: fenced\n```"}}
	planner := NewTaskPlanner(llm, testCatalog(), stubProfile{})

	out, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)

	assert.Equal(t, "name: fenced", strings.TrimSpace(out))
	assert.NotContains(t, out, "```")
}

// Templates() exposes what the planner will try, so the generator can iterate
// them without re-deriving the match.
func TestPlannerExposesItsTemplates(t *testing.T) {
	tmpl := DAGTemplate{Name: "a"}
	planner := NewTaskPlanner(&scriptedLLM{}, testCatalog(), stubProfile{templates: []DAGTemplate{tmpl}})

	templates := planner.Templates()
	require.Len(t, templates, 1)
	assert.Equal(t, "a", templates[0].Name)
}

// A nil profile means the generic one, which declares no templates — so the
// planner goes straight to the model rather than failing.
func TestPlannerWithNilProfileHasNoTemplates(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"name: generic\n"}}
	planner := NewTaskPlanner(llm, testCatalog(), nil)

	assert.Empty(t, planner.Templates())
	out, err := planner.Plan(context.Background(), &Requirement{Description: "x"})
	require.NoError(t, err)
	assert.Contains(t, out, "generic")
}

// --- scriptedLLM also needs to record the user message for the test above ---

func TestFixDAGStripsOnlyTheFence(t *testing.T) {
	assert.Equal(t, "name: x", fixDAG("```yaml\nname: x\n```"))
	assert.Equal(t, "name: x", fixDAG("```\nname: x\n```"))
	assert.Equal(t, "name: x", fixDAG("name: x"))
	assert.Equal(t, "name: x", fixDAG("  \n name: x \n "))
	assert.Empty(t, fixDAG(""))
}
