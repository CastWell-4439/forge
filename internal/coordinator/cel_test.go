package coordinator

import (
	"testing"
)

func TestCELEval_EmptyExpression(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}
	result, err := e.Eval("", nil)
	if err != nil {
		t.Fatalf("eval empty: %v", err)
	}
	if !result {
		t.Error("empty expression should return true")
	}
}

func TestCELEval_SimpleBool(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	ctx := map[string]any{
		"results": map[string]any{
			"review": map[string]any{"decision": "approve"},
		},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "wf_1",
	}

	result, err := e.Eval(`results.review.decision == "approve"`, ctx)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !result {
		t.Error("expected true")
	}
}

func TestCELEval_FalseCondition(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	ctx := map[string]any{
		"results": map[string]any{
			"review": map[string]any{"decision": "reject"},
		},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "wf_1",
	}

	result, err := e.Eval(`results.review.decision == "approve"`, ctx)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result {
		t.Error("expected false")
	}
}

func TestCELEval_IterationCheck(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	ctx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{},
		"iteration":   int64(3),
		"workflow_id": "wf_1",
	}

	result, err := e.Eval(`iteration >= 3`, ctx)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !result {
		t.Error("expected true for iteration >= 3")
	}
}

func TestCELEval_CompileError(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	_, err = e.Eval(`invalid $$$ syntax`, map[string]any{})
	if err == nil {
		t.Error("expected compile error")
	}
}

func TestCELEval_CachesPrograms(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	ctx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{"x": int64(5)},
		"iteration":   int64(0),
		"workflow_id": "",
	}

	// First call compiles
	_, err = e.Eval(`vars.x > 3`, ctx)
	if err != nil {
		t.Fatalf("first eval: %v", err)
	}

	// Second call should use cache
	result, err := e.Eval(`vars.x > 3`, ctx)
	if err != nil {
		t.Fatalf("second eval: %v", err)
	}
	if !result {
		t.Error("cached eval should return true")
	}

	e.mu.RLock()
	if len(e.programs) != 1 {
		t.Errorf("expected 1 cached program, got %d", len(e.programs))
	}
	e.mu.RUnlock()
}

func TestCELEvalString(t *testing.T) {
	e, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}

	ctx := map[string]any{
		"results": map[string]any{
			"step1": map[string]any{"status": "done"},
		},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "wf_1",
	}

	result, err := e.EvalString(`results.step1.status`, ctx)
	if err != nil {
		t.Fatalf("eval string: %v", err)
	}
	if result != "done" {
		t.Errorf("expected 'done', got %q", result)
	}
}
