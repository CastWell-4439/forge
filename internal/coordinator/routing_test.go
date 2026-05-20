package coordinator

import "testing"

func TestParseOnResult_Shorthand(t *testing.T) {
	raw := map[string]any{
		"success": "continue",
		"failure": "goto:retry_step",
		"timeout": "abort",
	}

	result, err := ParseOnResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if result["success"].Action != RouteActionContinue {
		t.Errorf("success: expected continue, got %s", result["success"].Action)
	}
	if result["failure"].Action != RouteActionGoto || result["failure"].Target != "retry_step" {
		t.Errorf("failure: expected goto:retry_step, got %s:%s", result["failure"].Action, result["failure"].Target)
	}
	if result["timeout"].Action != RouteActionAbort {
		t.Errorf("timeout: expected abort, got %s", result["timeout"].Action)
	}
}

func TestParseOnResult_FullForm(t *testing.T) {
	raw := map[string]any{
		"rejected": map[string]any{
			"action": "goto",
			"target": "notify_user",
		},
	}

	result, err := ParseOnResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if result["rejected"].Action != RouteActionGoto || result["rejected"].Target != "notify_user" {
		t.Errorf("rejected: got %+v", result["rejected"])
	}
}

func TestParseOnResult_InvalidShorthand(t *testing.T) {
	raw := map[string]any{
		"success": "unknown_action",
	}

	_, err := ParseOnResult(raw)
	if err == nil {
		t.Error("expected error for invalid shorthand")
	}
}

func TestParseOnResult_GotoMissingTarget(t *testing.T) {
	raw := map[string]any{
		"failure": "goto:",
	}

	_, err := ParseOnResult(raw)
	if err == nil {
		t.Error("expected error for goto without target")
	}
}

func TestParseOnResult_Nil(t *testing.T) {
	result, err := ParseOnResult(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil for nil input")
	}
}

func TestOnResult_Resolve(t *testing.T) {
	r := OnResult{
		"success": ResultRoute{Action: RouteActionContinue},
		"failure": ResultRoute{Action: RouteActionGoto, Target: "fix_step"},
	}

	// Known status
	route := r.Resolve("failure")
	if route.Action != RouteActionGoto || route.Target != "fix_step" {
		t.Errorf("failure route: %+v", route)
	}

	// Unknown status defaults to continue
	route = r.Resolve("unknown")
	if route.Action != RouteActionContinue {
		t.Errorf("unknown route: expected continue, got %s", route.Action)
	}

	// Nil OnResult defaults to continue
	var nilR OnResult
	route = nilR.Resolve("anything")
	if route.Action != RouteActionContinue {
		t.Errorf("nil route: expected continue, got %s", route.Action)
	}
}

func TestParseOnResult_Skip(t *testing.T) {
	raw := map[string]any{
		"no_changes": "skip",
	}

	result, err := ParseOnResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result["no_changes"].Action != RouteActionSkip {
		t.Errorf("expected skip, got %s", result["no_changes"].Action)
	}
}
