package integration

import (
	"context"
	"strings"
	"testing"
)

func TestNoopWebhookAdapter(t *testing.T) {
	_, err := NoopWebhookAdapter{}.Parse(context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "noop webhook adapter") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestNoopToolAdapter(t *testing.T) {
	resp, err := NoopToolAdapter{}.Call(context.Background(), ToolRequest{RunID: "run_1", ToolName: "demo.tool"})
	if err == nil || !strings.Contains(err.Error(), "noop tool adapter") {
		t.Fatalf("Call() error = %v", err)
	}
	if resp.RunID != "run_1" || resp.ToolName != "demo.tool" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.StartedAt.IsZero() || resp.EndedAt.IsZero() {
		t.Fatalf("response timestamps missing: %+v", resp)
	}
}

func TestValidateToolRequest(t *testing.T) {
	if err := ValidateToolRequest(ToolRequest{RunID: "run_1", ToolName: "demo.tool"}); err != nil {
		t.Fatalf("ValidateToolRequest() error = %v", err)
	}
	if err := ValidateToolRequest(ToolRequest{ToolName: "demo.tool"}); err == nil {
		t.Fatalf("ValidateToolRequest() expected missing run id error")
	}
	if err := ValidateToolRequest(ToolRequest{RunID: "run_1"}); err == nil {
		t.Fatalf("ValidateToolRequest() expected missing tool name error")
	}
}
