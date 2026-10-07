package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// nativeLLM speaks both protocols: ChatWithTools for the native path,
// ChatWithUsage/Chat for the prompt path (used by degrade and reflect).
type nativeLLM struct {
	// native sequence; empty entries fall through to prompts
	native      []core.ChatResult
	prompts     []string // contents returned for prompt-path calls
	nativeCalls [][]core.Message
	promptCalls [][]core.Message
	nativeIdx   int
	promptIdx   int
}

func (n *nativeLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	r, err := n.ChatWithUsage(ctx, messages)
	return r.Content, err
}

func (n *nativeLLM) ChatWithUsage(_ context.Context, messages []core.Message) (core.ChatResult, error) {
	n.promptCalls = append(n.promptCalls, messages)
	content := `{"thought":"t","answer":"done"}`
	if n.promptIdx < len(n.prompts) {
		content = n.prompts[n.promptIdx]
	}
	n.promptIdx++
	return core.ChatResult{Content: content, FinishReason: "stop"}, nil
}

func (n *nativeLLM) ChatWithTools(_ context.Context, messages []core.Message, _ []*core.ToolDef) (core.ChatResult, error) {
	n.nativeCalls = append(n.nativeCalls, messages)
	if n.nativeIdx >= len(n.native) {
		return core.ChatResult{}, ErrToolsUnsupported // exhausted → degrade path for the test
	}
	r := n.native[n.nativeIdx]
	n.nativeIdx++
	return r, nil
}

var _ ToolAwareLLM = (*nativeLLM)(nil)

// twoToolRegistry holds both tools so one batch can touch both.
func twoToolRegistry(t *testing.T, pushCount, lookupCount *int) *workers.ToolRegistry {
	t.Helper()
	reg := workers.NewToolRegistry()
	require.NoError(t, reg.Register(&core.ToolDef{
		Name: "test.push", Idempotent: false, Effect: core.EffectRead,
		InputSchema: map[string]core.ParamDef{"x": {Type: "integer", Required: true}},
	}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		*pushCount++
		return map[string]interface{}{"ok": true}, nil
	}))
	require.NoError(t, reg.Register(&core.ToolDef{
		Name: "test.lookup", Idempotent: true, Effect: core.EffectRead,
		InputSchema: map[string]core.ParamDef{"q": {Type: "string", Required: true}},
	}, func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		*lookupCount++
		return map[string]interface{}{"found": true}, nil
	}))
	return reg
}

// C1's fix at the loop level: one provider response carrying TWO tool calls
// is executed in full — the structure no longer caps a step at one call.
func TestNativeBatchExecutesAllCalls(t *testing.T) {
	var pushCount, lookupCount int
	reg := twoToolRegistry(t, &pushCount, &lookupCount)
	llm := &nativeLLM{native: []core.ChatResult{
		{
			FinishReason: "tool_calls",
			ToolCalls: []core.NativeToolCall{
				{ID: "c1", Name: "test.push", Arguments: map[string]any{"x": float64(1)}},
				{ID: "c2", Name: "test.lookup", Arguments: map[string]any{"q": "y"}},
			},
		},
		{Content: "all done", FinishReason: "stop"},
	}}

	loop := NewAgentLoop(llm, NewToolRouter(reg), LoopConfig{MaxSteps: 5, NativeTools: true})
	result, err := loop.Run(context.Background(), "native-batch", "do both")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, 1, pushCount, "the first call executed")
	assert.Equal(t, 1, lookupCount, "the second call executed — one step, two tools")
	require.NotEmpty(t, result.Steps)
	assert.Equal(t, "test.push", result.Steps[0].Action.Name)
	assert.Equal(t, "test.lookup", result.Steps[1].Action.Name)
	// The router adapts float64→int64 for integer params IN PLACE before the
	// handler sees them; the step records the same map, so what the test
	// observes is exactly what the handler received.
	assert.Equal(t, int64(1), result.Steps[0].Action.Params["x"], "arguments arrive as the handler got them")

	// History follows the native protocol: assistant with BOTH requests,
	// then one tool-role answer each — this is what the provider expects next.
	require.GreaterOrEqual(t, len(llm.nativeCalls), 2, "second call sees the history")
	history := llm.nativeCalls[1]
	var assistantWithCalls int
	toolAnswers := map[string]bool{}
	for _, m := range history {
		if m.Role == "assistant" && len(m.ToolCalls) == 2 {
			assistantWithCalls++
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			toolAnswers[m.ToolCallID] = m.Content != ""
		}
	}
	assert.Equal(t, 1, assistantWithCalls, "the batch turn is one assistant message with both requests")
	assert.Equal(t, map[string]bool{"c1": true, "c2": true}, toolAnswers,
		"each request is answered by its id")
}

// An endpoint that refuses the tools field downgrades the run to the prompt
// path mid-flight and still completes the task.
func TestNativeDegradesToPromptPath(t *testing.T) {
	var pushCount, lookupCount int
	reg := twoToolRegistry(t, &pushCount, &lookupCount)
	llm := &nativeLLM{
		native: nil, // first ChatWithTools → ErrToolsUnsupported
		prompts: []string{
			`{"thought":"t","action":{"name":"test.push","params":{"x":42}}}`,
			`{"thought":"done","answer":"finished"}`,
		},
	}

	loop := NewAgentLoop(llm, NewToolRouter(reg), LoopConfig{MaxSteps: 5, NativeTools: true})
	result, err := loop.Run(context.Background(), "native-degrade", "go")
	require.NoError(t, err, "a capability gap is not a run failure")

	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, "finished", result.Answer)
	assert.Equal(t, 1, pushCount, "the prompt path executed the tool")
	assert.GreaterOrEqual(t, len(llm.promptCalls), 2, "the run continued over the prompt path")
}

// The native terminal needs no JSON contract: the content IS the answer.
// The prompt path would have failed to parse this text.
func TestNativeTerminalTakesContentAsAnswer(t *testing.T) {
	var pushCount, lookupCount int
	reg := twoToolRegistry(t, &pushCount, &lookupCount)
	plain := "this is not JSON at all — and does not need to be"
	llm := &nativeLLM{native: []core.ChatResult{{Content: plain, FinishReason: "stop"}}}

	loop := NewAgentLoop(llm, NewToolRouter(reg), LoopConfig{MaxSteps: 5, NativeTools: true})
	result, err := loop.Run(context.Background(), "native-terminal", "answer me")
	require.NoError(t, err)

	assert.Equal(t, "completed", result.Reason)
	assert.Equal(t, plain, result.Answer)
	assert.Empty(t, llm.promptCalls, "no ParseWithRetry round trip happened")
}

// A native batch that ran through the journal rebuilds into the protocol
// shape: the assistant message keeps its requests, and the tool answers keep
// their correlation ids — a resumed conversation the provider still accepts.
func TestRebuildRestoresNativeTurn(t *testing.T) {
	var pushCount, lookupCount int
	reg := twoToolRegistry(t, &pushCount, &lookupCount)
	llm := &nativeLLM{native: []core.ChatResult{
		{FinishReason: "tool_calls", ToolCalls: []core.NativeToolCall{
			{ID: "c9", Name: "test.push", Arguments: map[string]any{"x": float64(7)}},
		}},
		{Content: "done", FinishReason: "stop"},
	}}

	j := pathJournal(t, t.TempDir())
	loop := NewAgentLoop(llm, NewToolRouter(reg), LoopConfig{MaxSteps: 5, NativeTools: true})
	loop.SetJournal(j)

	result, err := loop.Run(context.Background(), "native-journal", "run it")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	events, err := j.ReadEvents(context.Background(), "native-journal")
	require.NoError(t, err)
	cp, err := RebuildCheckpoint(events)
	require.NoError(t, err)

	var sawAssistant bool
	var sawToolAnswer bool
	for _, m := range cp.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "c9" {
			sawAssistant = true
			assert.Equal(t, float64(7), m.ToolCalls[0].Arguments["x"])
		}
		if m.Role == "tool" && m.ToolCallID == "c9" {
			sawToolAnswer = m.Content != ""
		}
	}
	assert.True(t, sawAssistant, "the rebuilt history keeps the assistant's request")
	assert.True(t, sawToolAnswer, "the rebuilt history keeps the tool's answer by id")

	require.NotEmpty(t, cp.ToolCalls)
	assert.Equal(t, float64(7), cp.ToolCalls[0].Params["x"],
		"the ledger records what the native call asked with")
}
