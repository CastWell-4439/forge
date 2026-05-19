package mcp

import (
	"context"
	"testing"
)

func TestWorker_Execute_UnknownAction(t *testing.T) {
	w := &Worker{} // nil client is fine for unknown action test
	_, err := w.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestWorker_Execute_MissingParams(t *testing.T) {
	w := &Worker{}

	cases := []struct {
		action string
		params map[string]any
	}{
		{"get_workitem", map[string]any{}},
		{"get_workitem", map[string]any{"project_key": "p1"}},
		{"search_workitems", map[string]any{"project_key": "p1"}},
		{"update_field", map[string]any{}},
		{"transition_state", map[string]any{}},
		{"add_comment", map[string]any{}},
		{"list_comments", map[string]any{}},
	}

	for _, c := range cases {
		_, err := w.Execute(context.Background(), c.action, c.params)
		if err == nil {
			t.Errorf("action %q with params %v: expected error", c.action, c.params)
		}
	}
}
