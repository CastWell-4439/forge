package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcore "github.com/castwell/forge/internal/agent"
	"github.com/castwell/forge/internal/agent/core"
)

// answerLLM always returns a terminal answer (no tools involved).
type answerLLM struct{}

func (answerLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := answerLLM{}.ChatWithUsage(ctx, messages)
	return result.Content, err
}

func (answerLLM) ChatWithUsage(_ context.Context, _ []core.Message) (core.ChatResult, error) {
	return core.ChatResult{Content: `{"thought":"t","answer":"the answer"}`}, nil
}

func newTestWorker(t *testing.T) *Worker {
	t.Helper()
	a := agentcore.New(answerLLM{},
		agentcore.WithHandlerMode("mock"),
		agentcore.WithWorkspace(t.TempDir()),
	)
	return NewWorker(a)
}

// The worker's contract: action = run, task in params, answer accounting
// out as JSON — the same string-out shape every workflow worker speaks.
func TestExecuteRunReturnsAnswerJSON(t *testing.T) {
	w := newTestWorker(t)

	out, err := w.Execute(context.Background(), "run", map[string]any{
		"task": "summarize the incident",
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "the answer", result["answer"])
	assert.Equal(t, "completed", result["reason"])
	assert.GreaterOrEqual(t, result["steps"].(float64), float64(1))
}

// Unknown actions say what is supported instead of failing obscurely.
func TestExecuteRejectsUnknownAction(t *testing.T) {
	w := newTestWorker(t)

	_, err := w.Execute(context.Background(), "chat", map[string]any{"task": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supported: run")
}

// The prompt param is an accepted alias; neither present is an error that
// names the parameter.
func TestExecuteRequiresTask(t *testing.T) {
	w := newTestWorker(t)

	_, err := w.Execute(context.Background(), "run", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task")

	// prompt alias works.
	out, err := w.Execute(context.Background(), "run", map[string]any{"prompt": "via alias"})
	require.NoError(t, err)
	assert.Contains(t, out, "the answer")
}
