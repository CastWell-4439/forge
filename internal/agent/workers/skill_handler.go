package workers

import (
	"context"
	"fmt"
	"time"
)

// Skill activate tool definition: skill.activate.
//
// This is the in-repo consumption side of the loop's last link: published
// SkillPacks (experience distilled from verified replays) become loadable
// context for the agent. Like ask.user, the loader is injected so this layer
// depends on no particular asset store.

// LoadSkillFunc loads a published skill by id and returns its document as text.
type LoadSkillFunc func(ctx context.Context, id string) (string, error)

func SkillActivateDef() *ToolDef {
	return &ToolDef{
		Name:        "skill.activate",
		DisplayName: "Skill Activate",
		Category:    "skill",
		Description: "Load a published SkillPack (verified experience distilled from real runs) and return its steps, constraints and readme as context. Use when the task matches a skill's trigger before improvising.",
		InputSchema: map[string]ParamDef{
			"id": {Type: "string", Description: "SkillPack id to load", Required: true},
		},
		OutputSchema: map[string]ParamDef{
			"content": {Type: "string", Description: "The skill document"},
		},
		RequiredParams: []string{"id"},
		Effect:         EffectRead,
		EstimatedTime:  1 * time.Second,
	}
}

func NewSkillActivateHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockSkillActivate()
	}
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		id, _ := params["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("skill.activate: missing required param 'id'")
		}
		if cfg.LoadSkill == nil {
			return nil, fmt.Errorf("skill.activate: %w (no skill loader in HandlerConfig)", ErrNotConfigured)
		}
		content, err := cfg.LoadSkill(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("skill.activate: %w", err)
		}
		if content == "" {
			return nil, fmt.Errorf("skill.activate: loader returned empty content for %q", id)
		}
		return map[string]interface{}{"content": content}, nil
	}
}

func mockSkillActivate() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		id, _ := params["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("skill.activate: missing required param 'id'")
		}
		return map[string]interface{}{
			"content": fmt.Sprintf("kind: SkillPack\nmetadata:\n  id: %s\nreadme: mock skill content\n", id),
		}, nil
	}
}
