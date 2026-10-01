package workers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSkillActivateMock(t *testing.T) {
	handler := NewSkillActivateHandler(HandlerConfig{Mode: HandlerModeMock})

	result, err := handler(context.Background(), map[string]interface{}{"id": "bugfix-triage"})
	if err != nil {
		t.Fatalf("mock skill.activate: %v", err)
	}
	content, _ := result["content"].(string)
	if content == "" || !strings.Contains(content, "bugfix-triage") {
		t.Errorf("mock content = %q, want the requested id", content)
	}

	if _, err := handler(context.Background(), map[string]interface{}{}); err == nil {
		t.Error("missing id should be an error")
	}
}

func TestSkillActivateRealNeedsALoader(t *testing.T) {
	handler := NewSkillActivateHandler(HandlerConfig{Mode: HandlerModeReal})

	_, err := handler(context.Background(), map[string]interface{}{"id": "x"})
	if err == nil {
		t.Fatal("a real-mode call without a loader must not invent a skill")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want the ErrNotConfigured family", err)
	}
}

func TestSkillActivateRealCallsTheLoader(t *testing.T) {
	var got string
	handler := NewSkillActivateHandler(HandlerConfig{
		Mode: HandlerModeReal,
		LoadSkill: func(_ context.Context, id string) (string, error) {
			got = id
			return "kind: SkillPack\nreadme: real content", nil
		},
	})

	result, err := handler(context.Background(), map[string]interface{}{"id": "probe"})
	if err != nil {
		t.Fatalf("real skill.activate: %v", err)
	}
	if got != "probe" {
		t.Errorf("loader received %q, want probe", got)
	}
	if result["content"] != "kind: SkillPack\nreadme: real content" {
		t.Errorf("content = %v", result["content"])
	}
}
