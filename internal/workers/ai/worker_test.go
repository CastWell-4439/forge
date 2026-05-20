package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

// mockLLMClient returns a canned answer for testing.
type mockLLMClient struct {
	response string
	err      error
}

func (m *mockLLMClient) Chat(ctx context.Context, messages []core.Message) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	// Return a structured agent response (answer mode).
	return fmt.Sprintf(`{"thought":"analyzing...","answer":"%s"}`, m.response), nil
}

func (m *mockLLMClient) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
	if m.err != nil {
		return core.ChatResult{}, m.err
	}
	resp := fmt.Sprintf(`{"thought":"analyzing...","answer":"%s"}`, m.response)
	return core.ChatResult{
		Content: resp,
		Usage:   core.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

func newTestWorker(response string, err error) *Worker {
	factory := func(cfg ModelConfig) (core.LLMClient, error) {
		return &mockLLMClient{response: response, err: err}, nil
	}
	return NewWorker(DefaultConfig(), factory)
}

func TestWorkerExecute_Analyze(t *testing.T) {
	w := newTestWorker("analysis complete", nil)
	result, err := w.Execute(context.Background(), "analyze", map[string]any{
		"prompt": "Analyze this codebase for security issues",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWorkerExecute_Synthesize(t *testing.T) {
	w := newTestWorker("synthesis done", nil)
	result, err := w.Execute(context.Background(), "synthesize", map[string]any{
		"prompt":  "Combine these reports",
		"context": "Report A: ... Report B: ...",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWorkerExecute_Classify(t *testing.T) {
	w := newTestWorker("category: bug, confidence: 0.9", nil)
	result, err := w.Execute(context.Background(), "classify", map[string]any{
		"prompt": "Is this a bug or a feature request?",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWorkerExecute_Summarize(t *testing.T) {
	w := newTestWorker("summary here", nil)
	result, err := w.Execute(context.Background(), "summarize", map[string]any{
		"prompt": "Summarize this meeting transcript",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWorkerExecute_GenerateCodePlan(t *testing.T) {
	w := newTestWorker("1. create file 2. implement", nil)
	result, err := w.Execute(context.Background(), "generate_code_plan", map[string]any{
		"prompt": "Add user authentication to the API",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWorkerExecute_UnknownAction(t *testing.T) {
	w := newTestWorker("", nil)
	_, err := w.Execute(context.Background(), "invalid_action", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("expected 'unknown action' error, got: %v", err)
	}
}

func TestWorkerExecute_LLMFactoryError(t *testing.T) {
	factory := func(cfg ModelConfig) (core.LLMClient, error) {
		return nil, fmt.Errorf("connection refused")
	}
	w := NewWorker(DefaultConfig(), factory)
	_, err := w.Execute(context.Background(), "analyze", map[string]any{
		"prompt": "test",
	})
	if err == nil {
		t.Fatal("expected error when LLM factory fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerExecute_NoPrompt(t *testing.T) {
	w := newTestWorker("ok", nil)
	// Empty params should still work (fallback to JSON serialization)
	result, err := w.Execute(context.Background(), "analyze", map[string]any{
		"key": "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestModelForAction(t *testing.T) {
	cfg := DefaultConfig()

	// Known action with override.
	m := cfg.ModelForAction("classify")
	if m.Temperature != 0.0 {
		t.Errorf("expected temperature 0.0 for classify, got %f", m.Temperature)
	}

	// Unknown action falls back to default.
	m = cfg.ModelForAction("unknown")
	if m.Temperature != cfg.DefaultModel.Temperature {
		t.Errorf("expected default temperature, got %f", m.Temperature)
	}
}
