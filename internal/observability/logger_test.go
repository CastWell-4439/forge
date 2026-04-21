package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlogLogger_JSON(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSlogLogger(WithFormat("json"), WithWriter(&buf), WithLevel(slog.LevelDebug))

	logger.Info("task started", "task_id", "t-001", "handler", "ai.tts")

	var entry map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "task started", entry["msg"])
	assert.Equal(t, "t-001", entry["task_id"])
	assert.Equal(t, "ai.tts", entry["handler"])
	assert.Equal(t, "INFO", entry["level"])
}

func TestSlogLogger_Text(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSlogLogger(WithFormat("text"), WithWriter(&buf))

	logger.Warn("retry attempt", "attempt", 3)

	output := buf.String()
	assert.Contains(t, output, "WARN")
	assert.Contains(t, output, "retry attempt")
	assert.Contains(t, output, "attempt=3")
}

func TestSlogLogger_Levels(t *testing.T) {
	var buf bytes.Buffer
	// Set level to Warn — Info and Debug should be filtered.
	logger := NewSlogLogger(WithFormat("json"), WithWriter(&buf), WithLevel(slog.LevelWarn))

	logger.Debug("should not appear")
	logger.Info("should not appear")
	assert.Empty(t, buf.String())

	logger.Warn("should appear")
	assert.NotEmpty(t, buf.String())
	assert.Contains(t, buf.String(), "should appear")
}

func TestSlogLogger_With(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSlogLogger(WithFormat("json"), WithWriter(&buf))

	child := logger.With("workflow_id", "wf-123")
	child.Info("step done", "step", 2)

	var entry map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "wf-123", entry["workflow_id"])
	assert.Equal(t, "step done", entry["msg"])
}

func TestSlogLogger_Context(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSlogLogger(WithFormat("json"), WithWriter(&buf))

	ctx := context.Background()
	logger.InfoContext(ctx, "with context", "key", "val")

	assert.Contains(t, buf.String(), "with context")
	assert.Contains(t, buf.String(), "key")
}

func TestStdLogger(t *testing.T) {
	logger := NewStdLogger()

	// Just verify it doesn't panic.
	logger.Info("hello", "key", "value")
	logger.Warn("warning")
	logger.Error("err", "code", 42)
	logger.Debug("debug msg")
}

func TestStdLogger_With(t *testing.T) {
	logger := NewStdLogger()
	child := logger.With("component", "scheduler")
	child.Info("tick")
	// No assertion — just verify no panic.
}

func TestNopLogger(t *testing.T) {
	logger := Nop()
	logger.Info("nothing")
	logger.Warn("nothing")
	logger.Error("nothing")
	logger.Debug("nothing")
	logger.InfoContext(context.Background(), "nothing")

	child := logger.With("key", "val")
	child.Info("still nothing")
}

func TestFormatArgs(t *testing.T) {
	assert.Equal(t, "", formatArgs(nil))
	assert.Equal(t, "k=v", formatArgs([]any{"k", "v"}))
	assert.Equal(t, "a=1 b=2", formatArgs([]any{"a", 1, "b", 2}))
	// Odd args — trailing key gets "?"
	assert.True(t, strings.Contains(formatArgs([]any{"x"}), "x=?"))
}
