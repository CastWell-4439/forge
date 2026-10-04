package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// scriptedLLM plays back canned responses and records the messages each call
// received — enough to observe which tool-mode a run executed under.
type scriptedLLM struct {
	responses []string
	calls     [][]core.Message
	idx       int
}

func (s *scriptedLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	result, err := s.ChatWithUsage(ctx, messages)
	return result.Content, err
}

func (s *scriptedLLM) ChatWithUsage(_ context.Context, messages []core.Message) (core.ChatResult, error) {
	recorded := make([]core.Message, len(messages))
	copy(recorded, messages)
	s.calls = append(s.calls, recorded)

	if s.idx >= len(s.responses) {
		return core.ChatResult{Content: s.responses[len(s.responses)-1]}, nil
	}
	response := s.responses[s.idx]
	s.idx++
	return core.ChatResult{Content: response}, nil
}

const readToolCall = `{"thought":"read a file","action":{"name":"file.read","params":{"path":"nope.txt"}}}`
const finalAnswer = `{"thought":"done","answer":"ok"}`

// The workspace is created on demand — without this, real mode's first
// file/shell call would fail with "directory does not exist" because
// nobody else ever created the hardcoded directory.
func TestRunCreatesWorkspaceOnDemand(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent-ws")
	llm := &scriptedLLM{responses: []string{finalAnswer}}
	a := New(llm, WithWorkspace(ws))

	result, err := a.Run(context.Background(), "session-ws", "hi")
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Reason)

	info, err := os.Stat(ws)
	require.NoError(t, err, "workspace must exist after a run")
	assert.True(t, info.IsDir())
}

// The mode option reaches the tools: under mock the observation carries the
// canned "mock content", under the real default it does not — the old
// hardcoded mock made this distinction impossible.
func TestHandlerModeReachesTheTools(t *testing.T) {
	readTool := func(t *testing.T, opts ...Option) *scriptedLLM {
		t.Helper()
		llm := &scriptedLLM{responses: []string{readToolCall, finalAnswer}}
		a := New(llm, append(opts, WithWorkspace(t.TempDir()))...)
		_, err := a.Run(context.Background(), "session-mode", "read it")
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(llm.calls), 2, "tool call then answer")
		return llm
	}

	mockLLM := readTool(t, WithHandlerMode(workers.HandlerModeMock))
	mockObs := messageText(mockLLM.calls[1])
	assert.Contains(t, mockObs, "mock content", "mock mode returns canned tool output")

	realLLM := readTool(t) // real is the zero-value default
	assert.NotContains(t, messageText(realLLM.calls[1]), "mock content",
		"real mode must not return canned output")
}

// messageText flattens a message batch for substring assertions.
func messageText(msgs []core.Message) string {
	out := ""
	for _, m := range msgs {
		out += m.Content + "\n"
	}
	return out
}
