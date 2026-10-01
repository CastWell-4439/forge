package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// Regression tests for defects found in review. Each test names the behaviour
// that used to be wrong so a future change cannot silently reintroduce it.

// scriptedLLM returns queued ChatResults and records every message list it was
// asked to complete, so tests can inspect what the model actually saw.
type scriptedLLM struct {
	results []core.ChatResult
	errs    []error
	calls   [][]core.Message
	idx     int
}

func (m *scriptedLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := m.ChatWithUsage(ctx, messages)
	return result.Content, err
}

func (m *scriptedLLM) ChatWithUsage(_ context.Context, messages []core.Message) (core.ChatResult, error) {
	m.calls = append(m.calls, append([]core.Message(nil), messages...))

	i := m.idx
	if i >= len(m.results) {
		i = len(m.results) - 1
	}
	m.idx++

	if i < len(m.errs) && m.errs[i] != nil {
		return core.ChatResult{}, m.errs[i]
	}
	return m.results[i], nil
}

// recordingBudget captures recorded token usage.
type recordingBudget struct {
	recorded int64
}

func (b *recordingBudget) Check(context.Context, string) error { return nil }

func (b *recordingBudget) Record(_ context.Context, _ string, tokens int64) error {
	b.recorded += tokens
	return nil
}

// A response cut off by the token limit must not be accepted as a complete
// answer; it used to be parsed as if it were finished.
func TestAgentLoop_TruncatedResponseIsRejected(t *testing.T) {
	llm := &scriptedLLM{results: []core.ChatResult{{
		Content:      `{"thought": "the JSON is cut off here`,
		FinishReason: "length",
		Usage:        core.TokenUsage{CompletionTokens: 4096, TotalTokens: 4200},
	}}}

	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	result, err := loop.Run(context.Background(), "sess", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
	require.NotNil(t, result, "a failed run must still return its trace")
	assert.Equal(t, "truncated", result.Reason)
}

// Token usage reported by the provider must reach the budget checker,
// otherwise usage stays at zero and the budget can never trip.
func TestAgentLoop_RecordsTokenUsage(t *testing.T) {
	llm := &scriptedLLM{results: []core.ChatResult{{
		Content: `{"thought": "done", "answer": "ok"}`,
		Usage:   core.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
	}}}

	budget := &recordingBudget{}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetBudget(budget)

	_, err := loop.Run(context.Background(), "sess", "hi")
	require.NoError(t, err)
	assert.EqualValues(t, 120, budget.recorded, "total tokens must be recorded")
}

// A mid-run LLM failure used to return a nil result, discarding every step
// accumulated so far and making the failure impossible to diagnose.
func TestAgentLoop_LLMErrorPreservesAccumulatedSteps(t *testing.T) {
	llm := &scriptedLLM{
		results: []core.ChatResult{
			{Content: `{"thought": "probe it", "action": {"name": "test.echo", "params": {"msg": "x"}}}`},
			{},
		},
		errs: []error{nil, errors.New("simulated transport failure")},
	}

	registry := newRegistryWith("test.echo", "Echoes input",
		func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"echo": params["msg"]}, nil
		},
	)
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())

	result, err := loop.Run(context.Background(), "sess", "go")
	require.Error(t, err)
	require.NotNil(t, result, "the partial trace must survive an LLM error")
	assert.Equal(t, "error", result.Reason)
	require.Len(t, result.Steps, 1, "the completed tool step must be preserved")
	assert.Equal(t, "test.echo", result.Steps[0].Action.Name)
}

// The assistant turn echoed back into the conversation used to keep only the
// tool name, so the model could not see which arguments it had just supplied.
func TestAgentLoop_AssistantTurnKeepsToolParams(t *testing.T) {
	llm := &scriptedLLM{results: []core.ChatResult{
		{Content: `{"thought": "probe", "action": {"name": "test.echo", "params": {"msg": "hello", "n": 2}}}`},
		{Content: `{"thought": "done", "answer": "ok"}`},
	}}

	registry := newRegistryWith("test.echo", "Echoes input",
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
	)
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())

	_, err := loop.Run(context.Background(), "sess", "go")
	require.NoError(t, err)
	require.Len(t, llm.calls, 2, "expected one call per loop iteration")

	var assistantTurns []string
	for _, m := range llm.calls[1] {
		if m.Role == "assistant" {
			assistantTurns = append(assistantTurns, m.Content)
		}
	}
	require.Len(t, assistantTurns, 1)
	assert.Contains(t, assistantTurns[0], `"msg":"hello"`,
		"the assistant turn must carry the original tool params")
	assert.Contains(t, assistantTurns[0], `"n":2`)
}

// truncateToolResults used to rewrite the caller's slice in place and report a
// length that double-counted the truncation.
func TestTruncateToolResults_NoMutationAndAccurateCount(t *testing.T) {
	cm := NewContextManager(100, nil)

	// Multi-byte content: 3 bytes per rune, so a plain byte slice at the limit
	// would split a rune.
	original := strings.Repeat("あ", maxToolResultChars)
	messages := []core.Message{{Role: "user", Content: original}}

	out := cm.truncateToolResults(messages)

	require.Len(t, out, 1)
	assert.Equal(t, original, messages[0].Content,
		"the caller's slice must not be mutated in place")
	assert.Less(t, len(out[0].Content), len(original))
	assert.True(t, utf8.ValidString(out[0].Content),
		"truncation must not split a UTF-8 sequence")
	assert.Contains(t, out[0].Content, fmt.Sprintf("original was %d chars", len(original)),
		"the reported length must be the original, not a double count")
}

func TestTruncateAtRuneBoundary(t *testing.T) {
	// "日本語" is 9 bytes; cutting at 4 would land inside the second rune.
	assert.Equal(t, "日", truncateAtRuneBoundary("日本語", 4))
	assert.Equal(t, "日本", truncateAtRuneBoundary("日本語", 6))
	assert.Equal(t, "日本語", truncateAtRuneBoundary("日本語", 9))
	assert.Equal(t, "abcdef", truncateAtRuneBoundary("abcdef", 100))
}

func TestIsRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		408: true, // request timeout
		409: true, // conflict
		429: true, // rate limited
		500: true,
		503: true,
		400: false, // a plain bad request cannot succeed on retry
		413: false, // resending the same oversized payload cannot help
		401: false,
		404: false,
	}
	for code, want := range cases {
		assert.Equal(t, want, isRetryableStatus(code), "status %d", code)
	}
}

func TestIsContextOverflow(t *testing.T) {
	assert.True(t, isContextOverflow(400,
		[]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens"}}`)))
	assert.True(t, isContextOverflow(413, []byte("request too large")))
	assert.True(t, isContextOverflow(400, []byte("prompt is too long: 200000 tokens")))

	assert.False(t, isContextOverflow(400, []byte(`{"error":"invalid api key"}`)))
	assert.False(t, isContextOverflow(429, []byte("context length")))
	assert.False(t, isContextOverflow(500, []byte("context length")))
}
