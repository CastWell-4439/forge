package planning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCatalog is the workflow handler set these tests validate against. It is
// hand-written rather than taken from the workers package so the validator's
// behaviour is tested against a known set: pulling in the real registry would
// make a failure here ambiguous between the validator and the registry.
func testCatalog() *HandlerCatalog {
	return NewHandlerCatalog([]HandlerSpec{
		AgentHandlerSpec(),
		{Name: "shell", Description: "run a whitelisted command", Required: []string{"action"}},
		{Name: "review", Description: "review a plan or diff", Required: []string{"action"}},
	})
}

func TestExtractYAML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		isEmpty  bool
	}{
		{
			name:     "plain YAML",
			input:    "name: test\ntasks:\n  t1:\n    handler: agent",
			contains: "name: test",
		},
		{
			name:     "markdown yaml fence",
			input:    "```yaml\nname: test\ntasks:\n  t1:\n    handler: agent\n```",
			contains: "name: test",
		},
		{
			name:     "markdown generic fence",
			input:    "```\nname: test\ntasks:\n  t1:\n    handler: agent\n```",
			contains: "name: test",
		},
		{
			name:     "leading text",
			input:    "Here is the DAG:\nname: test\ntasks:\n  t1:\n    handler: agent",
			contains: "name: test",
		},
		{
			name:     "tabs to spaces",
			input:    "name: test\ntasks:\n\tt1:\n\t\thandler: agent",
			contains: "name: test",
		},
		{
			name:    "empty string",
			input:   "",
			isEmpty: true,
		},
		{
			name:    "whitespace only",
			input:   "   \n\n  ",
			isEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractYAML(tc.input)
			if tc.isEmpty {
				assert.Empty(t, result)
				return
			}
			assert.Contains(t, result, tc.contains)
		})
	}
}

// A DAG naming a handler this deployment cannot dispatch is rejected at L3.
//
// This is the check that was broken: it used to consult the agent's tool
// registry, where `web.fetch` exists — so a DAG naming a tool as a handler
// passed validation and then had no worker to run it. The catalog holds what the
// workflow can dispatch, which is the question that matters.
func TestValidateRejectsHandlerOutsideTheCatalog(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: tool-as-handler
tasks:
  fetch:
    handler: web.fetch
`)

	assert.False(t, result.Valid)
	require.NotEmpty(t, result.Issues)
	assert.Equal(t, "L3", result.Issues[0].Level)
	assert.Contains(t, result.Issues[0].Message, "unknown handler")
}

// A handler in the catalog passes L3.
func TestValidateAcceptsCatalogHandlers(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: known-handlers
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`)

	assert.True(t, result.Valid, "issues: %v", result.Issues)
	require.NotNil(t, result.DAG)
}

// An executor step with nothing to do is rejected at L4.
//
// Presence is not enough for these params: `task: ""` satisfies "the key exists"
// and gives the executor no work, so a dispatched task would fail at start
// instead of here.
func TestValidateRejectsEmptyAgentTask(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: empty-task
tasks:
  work:
    handler: agent
    params:
      action: run
      task: ""
`)

	assert.False(t, result.Valid)
	var found bool
	for _, issue := range result.Issues {
		if issue.Level == "L4" && issue.Severity == SeverityError {
			found = true
			assert.Contains(t, issue.Message, "empty")
		}
	}
	assert.True(t, found, "an empty task must be an L4 error, got %v", result.Issues)
}

// An executor step missing `task` entirely is rejected.
func TestValidateRejectsMissingAgentTask(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: no-task
tasks:
  work:
    handler: agent
    params:
      action: run
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "task")
}

// A declared-but-blank acceptance is rejected rather than treated as absent: it
// looks specified and is not, which is worse than saying nothing.
func TestValidateRejectsEmptyAcceptance(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: blank-acceptance
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
      acceptance: "   "
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "acceptance")
}

// Omitting acceptance is fine: a step whose outcome is obvious from its task
// does not need one repeated.
func TestValidateAllowsAbsentAcceptance(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: no-acceptance
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`)

	assert.True(t, result.Valid, "issues: %v", result.Issues)
}

// Required params declared by a handler are enforced.
func TestValidateRequiresDeclaredParams(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: missing-required
tasks:
  run:
    handler: shell
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "action")
}

// A cycle is an L3 error, reported through the coordinator's own structural
// validation rather than reimplemented here.
func TestValidateDetectsCycle(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: cyclic
tasks:
  a:
    handler: shell
    params:
      action: run
    depends_on: [b]
  b:
    handler: shell
    params:
      action: run
    depends_on: [a]
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "structural")
}

// A dangling dependency is an L3 error.
func TestValidateDetectsDanglingDependency(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: dangling
tasks:
  a:
    handler: shell
    params:
      action: run
    depends_on: [nonexistent]
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "structural")
}

// Empty or unparseable input fails at L1 or L2 without reaching L3.
func TestValidateStopsAtFormatAndSchema(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	t.Run("nothing to parse", func(t *testing.T) {
		result := v.Validate("   ")
		assert.False(t, result.Valid)
		assert.Equal(t, "L1", result.Issues[0].Level)
		assert.Nil(t, result.DAG)
	})

	t.Run("unparseable", func(t *testing.T) {
		result := v.Validate("name: x\ntasks: [this is not a map]")
		assert.False(t, result.Valid)
		assert.Equal(t, "L2", result.Issues[0].Level)
	})

	t.Run("no tasks", func(t *testing.T) {
		result := v.Validate("name: empty\n")
		assert.False(t, result.Valid)
		assert.Equal(t, "L2", result.Issues[0].Level)
	})

	t.Run("task without a handler", func(t *testing.T) {
		result := v.Validate("name: x\ntasks:\n  t:\n    params:\n      a: b\n")
		assert.False(t, result.Valid)
		assert.Contains(t, result.ErrorSummary(), "handler")
	})
}

// A nil catalog rejects everything: a validator that cannot tell what exists
// must not approve what it cannot check.
func TestValidateWithNilCatalogRejectsEverything(t *testing.T) {
	v := NewDAGValidator(nil)

	result := v.Validate(`
name: x
tasks:
  work:
    handler: agent
    params:
      action: run
      task: 做点什么
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "unknown handler")
}

// A close-but-wrong handler name gets a suggestion, so a retry prompt can point
// at the right name.
func TestValidateSuggestsCloseHandlerNames(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: typo
tasks:
  work:
    handler: agent.run
    params:
      task: 做点什么
`)

	assert.False(t, result.Valid)
	assert.Contains(t, result.ErrorSummary(), "did you mean")
	assert.Contains(t, result.ErrorSummary(), "agent")
}

// The four layers are labelled, so a retry prompt can say which one failed and a
// reader can tell a formatting slip from a semantic mistake.
func TestValidationIssuesCarryLevels(t *testing.T) {
	v := NewDAGValidator(testCatalog())

	result := v.Validate(`
name: levels
tasks:
  a:
    handler: not-a-handler
    params:
      action: run
`)
	require.False(t, result.Valid)

	levels := map[string]bool{}
	for _, issue := range result.Issues {
		levels[issue.Level] = true
	}
	assert.True(t, levels["L3"], "an unknown handler is an L3 issue")
}

// ErrorSummary reports only errors: warnings are advice, and feeding advice back
// as a failure would send the model chasing things that were already fine.
func TestErrorSummaryExcludesWarnings(t *testing.T) {
	r := &ValidationResult{
		Issues: []ValidationIssue{
			{Level: "L3", Severity: SeverityError, Message: "hard problem"},
			{Level: "L4", Severity: SeverityWarning, Message: "soft advice"},
		},
	}
	summary := r.ErrorSummary()
	assert.Contains(t, summary, "hard problem")
	assert.NotContains(t, summary, "soft advice")
}

// The catalog's prompt rendering carries the required params, because a model
// cannot infer them from a name and would otherwise produce tasks that L4 rejects.
func TestCatalogFormatForPrompt(t *testing.T) {
	out := testCatalog().FormatForPrompt()

	assert.Contains(t, out, "agent")
	assert.Contains(t, out, "shell")
	assert.Contains(t, out, "action", "required params must be stated, not implied")

	assert.Equal(t, "(no handlers available)", (*HandlerCatalog)(nil).FormatForPrompt())
}

// The catalog answers existence and lookup, and reports absence honestly rather
// than returning a zero spec that looks real.
func TestCatalogLookup(t *testing.T) {
	c := testCatalog()

	assert.True(t, c.Has("agent"))
	assert.False(t, c.Has("web.fetch"))

	spec, ok := c.Spec("agent")
	require.True(t, ok)
	assert.Equal(t, "agent", spec.Name)

	_, ok = c.Spec("nope")
	assert.False(t, ok, "a missing handler must be distinguishable from an empty spec")

	assert.Equal(t, []string{"agent", "review", "shell"}, c.Names())
}
