package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

func TestParseRequirement(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{
		"description": "把季度数据整理成一份报告",
		"fields": {"scope": "2026 Q1", "audience": "管理层"},
		"acceptance": {
			"criteria": "报告覆盖全部指标且数字与源数据一致",
			"checks": ["每个指标都有数值", "结论有数据支撑"]
		}
	}`}}

	parser := NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "帮我整理一份 Q1 的数据报告给管理层")
	require.NoError(t, err)

	assert.Equal(t, "把季度数据整理成一份报告", req.Description)
	assert.Equal(t, "2026 Q1", req.Fields["scope"])
	assert.Equal(t, "管理层", req.Fields["audience"])
	assert.Equal(t, "报告覆盖全部指标且数字与源数据一致", req.Acceptance.Criteria)
	assert.Len(t, req.Acceptance.Checks, 2)
}

// The requirement carries whatever the domain extracted, without the parser
// interpreting it. That is what keeps the engine domain-neutral: it forwards
// these values and never reads their meaning.
func TestParseKeepsDomainFieldsOpaque(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{
		"description": "x",
		"fields": {"anything_at_all": {"nested": [1, 2, 3]}}
	}`}}

	parser := NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "x")
	require.NoError(t, err)

	nested, ok := req.Fields["anything_at_all"].(map[string]any)
	require.True(t, ok, "an unknown field shape must survive intact")
	assert.NotNil(t, nested["nested"])
}

// An empty description falls back to the user's own words: a parser that loses
// the question has failed even when it produced valid JSON.
func TestParseFallsBackToUserTextForDescription(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{"fields": {}}`}}

	parser := NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "原始的那句话")
	require.NoError(t, err)

	assert.Equal(t, "原始的那句话", req.Description)
}

// Missing fields become an empty map, never nil: callers read from it, and a nil
// map is a panic waiting for the first write.
func TestParseAlwaysAllocatesFields(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{"description": "只有描述"}`}}

	parser := NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "x")
	require.NoError(t, err)

	require.NotNil(t, req.Fields)
	req.Fields["written"] = true // must not panic
}

// JSON wrapped in a markdown fence is still extracted.
func TestParseExtractsJSONFromFences(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"```json\n{\"description\": \"在栅栏里\"}\n```"}}

	parser := NewRequirementParser(llm, nil)
	req, err := parser.Parse(context.Background(), "x")
	require.NoError(t, err)

	assert.Equal(t, "在栅栏里", req.Description)
}

// Unparseable output is an error. Guessing a requirement from prose the model
// did not format would put invented values into everything downstream.
func TestParseRejectsUnparseableOutput(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"这根本不是 JSON"}}

	parser := NewRequirementParser(llm, nil)
	_, err := parser.Parse(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON")
}

// An unreachable model is reported, not swallowed.
func TestParseReportsLLMFailure(t *testing.T) {
	llm := &scriptedLLM{err: errors.New("network is down")}

	parser := NewRequirementParser(llm, nil)
	_, err := parser.Parse(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network is down")
}

// The parser sends the profile's own instruction: what to extract is the
// domain's business, and the engine never inspects the result. This test is what
// proves the seam is used rather than the engine quietly carrying its own prompt.
func TestParseUsesTheProfilesPrompt(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{"description": "x"}`}}
	profile := recordingProfile{}

	parser := NewRequirementParser(llm, profile)
	_, err := parser.Parse(context.Background(), "x")
	require.NoError(t, err)

	assert.Contains(t, llm.lastSystemPrompt, recordingMarker,
		"the profile's prompt must be what the model sees")
	assert.Equal(t, "recorder", parser.ProfileName())
}

// The generic profile is used when none is supplied: the engine has to work for
// a requirement nobody has written a domain for.
func TestParseNilProfileUsesGeneric(t *testing.T) {
	llm := &scriptedLLM{replies: []string{`{"description": "x"}`}}

	parser := NewRequirementParser(llm, nil)
	_, err := parser.Parse(context.Background(), "x")
	require.NoError(t, err)

	assert.Equal(t, "generic", parser.ProfileName())
	assert.Contains(t, llm.lastSystemPrompt, "需求分析师")
}

// --- a profile that proves the seam ---

const recordingMarker = "RECORDING-PROFILE-PROMPT"

type recordingProfile struct{}

func (recordingProfile) Name() string              { return "recorder" }
func (recordingProfile) ParseSystemPrompt() string { return recordingMarker }
func (recordingProfile) PlanHints() string         { return "hints" }
func (recordingProfile) Templates() []DAGTemplate  { return nil }

// The generic prompt must ask for exactly what the engine uses, and must tell
// the model not to invent an acceptance the user never gave.
func TestGenericProfilePromptIsHonest(t *testing.T) {
	prompt := GenericProfile{}.ParseSystemPrompt()

	assert.Contains(t, prompt, "description")
	assert.Contains(t, prompt, "acceptance")
	assert.Contains(t, prompt, "criteria")
	assert.Contains(t, prompt, "checks")
	assert.Contains(t, prompt, "不要替用户发明标准",
		"an invented acceptance would make a run look judged when nothing was specified")
}

// The generic profile declares no templates and no hints: a shape worth
// pre-building has to come from a domain that knows it recurs.
func TestGenericProfileDeclaresNothingExtra(t *testing.T) {
	p := GenericProfile{}
	assert.Empty(t, p.PlanHints())
	assert.Empty(t, p.Templates())
	assert.Equal(t, "generic", p.Name())
}

var _ = core.Message{}
