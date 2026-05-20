package review

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

type mockLLM struct {
	response string
}

func (m *mockLLM) Chat(ctx context.Context, messages []core.Message) (string, error) {
	return fmt.Sprintf(`{"thought":"reviewing...","answer":"%s"}`, m.response), nil
}

func (m *mockLLM) ChatWithUsage(ctx context.Context, messages []core.Message) (core.ChatResult, error) {
	return core.ChatResult{
		Content: fmt.Sprintf(`{"thought":"reviewing...","answer":"%s"}`, m.response),
		Usage:   core.TokenUsage{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
	}, nil
}

func TestExecute_ReviewPlan(t *testing.T) {
	w := NewWorker(DefaultConfig(), &mockLLM{response: "APPROVE - plan looks good"}, nil)
	result, err := w.Execute(context.Background(), "review_plan", map[string]any{
		"plan": "1. Add login endpoint\n2. Add JWT middleware\n3. Add tests",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestExecute_ReviewCode(t *testing.T) {
	w := NewWorker(DefaultConfig(), &mockLLM{response: "REQUEST_CHANGES - missing error handling"}, nil)
	result, err := w.Execute(context.Background(), "review_code", map[string]any{
		"diff": "+ func handler() {\n+   db.Query(sql)\n+ }",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestExecute_ReviewSecurity(t *testing.T) {
	w := NewWorker(DefaultConfig(), &mockLLM{response: "CRITICAL - SQL injection found"}, nil)
	result, err := w.Execute(context.Background(), "review_security", map[string]any{
		"diff": "+ query := fmt.Sprintf(\"SELECT * FROM users WHERE id = %s\", userInput)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	w := NewWorker(DefaultConfig(), &mockLLM{response: ""}, nil)
	_, err := w.Execute(context.Background(), "invalid", map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecute_MissingParam(t *testing.T) {
	w := NewWorker(DefaultConfig(), &mockLLM{response: ""}, nil)
	_, err := w.Execute(context.Background(), "review_plan", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing plan param")
	}
}

func TestBuildPrompts(t *testing.T) {
	p := buildReviewPlanPrompt("add auth", "conventions...")
	if !strings.Contains(p, "add auth") {
		t.Error("plan prompt missing content")
	}

	p = buildReviewCodePrompt("+ new code", "plan context", "standards")
	if !strings.Contains(p, "new code") || !strings.Contains(p, "plan context") {
		t.Error("code prompt missing content")
	}

	p = buildSecurityReviewPrompt("+ eval(input)", "security docs")
	if !strings.Contains(p, "eval(input)") {
		t.Error("security prompt missing content")
	}
}
