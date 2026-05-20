package coordinator

import "testing"

func TestLoopState_CanIterate(t *testing.T) {
	ls := NewLoopState()

	// First iteration should be allowed
	can, count, err := ls.CanIterate("step_a", 3)
	if err != nil || !can || count != 0 {
		t.Errorf("first iteration: can=%v, count=%d, err=%v", can, count, err)
	}

	// Increment to 3
	ls.Increment("step_a") // 1
	ls.Increment("step_a") // 2
	ls.Increment("step_a") // 3

	// 4th should be blocked
	can, count, err = ls.CanIterate("step_a", 3)
	if can || err == nil {
		t.Errorf("should be blocked at max: can=%v, count=%d, err=%v", can, count, err)
	}
}

func TestLoopState_DefaultMax(t *testing.T) {
	ls := NewLoopState()

	// maxIter=0 should use default (10)
	can, _, _ := ls.CanIterate("step_b", 0)
	if !can {
		t.Error("should allow with default max")
	}
}

func TestLoopState_AbsoluteMax(t *testing.T) {
	ls := NewLoopState()

	// Even if config says 999, cap at 100
	for i := 0; i < 100; i++ {
		ls.Increment("step_c")
	}
	can, _, _ := ls.CanIterate("step_c", 999)
	if can {
		t.Error("should be blocked at absolute max 100")
	}
}

func TestLoopState_Reset(t *testing.T) {
	ls := NewLoopState()
	ls.Increment("step_d")
	ls.Increment("step_d")
	ls.Reset("step_d")

	can, count, _ := ls.CanIterate("step_d", 3)
	if !can || count != 0 {
		t.Errorf("after reset: can=%v, count=%d", can, count)
	}
}

func TestLoopState_ResolveGoto(t *testing.T) {
	ls := NewLoopState()
	cel, _ := NewCELEvaluator()

	ctx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{},
		"iteration":   int64(0),
		"workflow_id": "",
	}

	// Should goto (no break condition, within limit)
	should, err := ls.ResolveGoto("step_e", 5, "", cel, ctx)
	if err != nil || !should {
		t.Errorf("first goto: should=%v, err=%v", should, err)
	}

	// Increment step_f a few times to make iteration >= 1
	ls.Increment("step_f") // count becomes 1

	// With break condition that evaluates to true (should NOT goto)
	// CanIterate returns count=1, ResolveGoto sets ctx["iteration"]=1
	should, err = ls.ResolveGoto("step_f", 5, "iteration >= 1", cel, ctx)
	if err != nil {
		t.Fatalf("break condition: %v", err)
	}
	if should {
		t.Error("should not goto when break condition is true")
	}
}

func TestLoopState_ResolveGoto_MaxReached(t *testing.T) {
	ls := NewLoopState()
	cel, _ := NewCELEvaluator()

	// Fill to max
	for i := 0; i < 3; i++ {
		ls.Increment("step_g")
	}

	ctx := map[string]any{
		"results":     map[string]any{},
		"vars":        map[string]any{},
		"iteration":   int64(3),
		"workflow_id": "",
	}

	should, err := ls.ResolveGoto("step_g", 3, "", cel, ctx)
	if should {
		t.Error("should not goto at max iterations")
	}
	if err == nil {
		t.Error("expected error at max iterations")
	}
}

func TestLoopState_IndependentTasks(t *testing.T) {
	ls := NewLoopState()
	ls.Increment("task_x")
	ls.Increment("task_x")

	// task_y should be unaffected
	can, count, _ := ls.CanIterate("task_y", 3)
	if !can || count != 0 {
		t.Errorf("task_y should be independent: can=%v, count=%d", can, count)
	}
}
