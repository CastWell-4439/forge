package workers

import (
	"context"
	"fmt"
	"time"
)

// AskUser handler tool definition: ask.user.
//
// This is the agent-side half of the human-in-the-loop story: when a task is
// underspecified, the agent asks instead of guessing. The answer comes from
// HandlerConfig.AskUser, so the agent layer stays free of any particular
// approval system - whoever assembles the agent decides what backs it.

func AskUserDef() *ToolDef {
	return &ToolDef{
		Name:        "ask.user",
		DisplayName: "Ask User",
		Category:    "human",
		Description: "Ask the human a question and wait for their answer. Use when a decision is underspecified and guessing would be worse; offer concrete options when possible.",
		InputSchema: map[string]ParamDef{
			"question": {Type: "string", Description: "The question to ask", Required: true},
			"options":  {Type: "array", Description: "Suggested answer options (optional)"},
			"context":  {Type: "string", Description: "Why you are asking (optional)"},
		},
		OutputSchema: map[string]ParamDef{
			"answer": {Type: "string", Description: "The human's answer"},
		},
		RequiredParams: []string{"question"},
		EstimatedTime:  30 * time.Second,
		Timeout:        0, // bounded by the human's response time, not by a default
	}
}

func NewAskUserHandler(cfg HandlerConfig) HandlerFunc {
	if cfg.Mode == HandlerModeMock {
		return mockAskUser()
	}
	return func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		question, _ := params["question"].(string)
		if question == "" {
			return nil, fmt.Errorf("ask.user: missing required param 'question'")
		}
		if cfg.AskUser == nil {
			return nil, fmt.Errorf("ask.user: %w (no interactive channel in HandlerConfig)", ErrNotConfigured)
		}
		options := stringSlice(params["options"])
		answer, err := cfg.AskUser(ctx, question, options)
		if err != nil {
			return nil, fmt.Errorf("ask.user: %w", err)
		}
		if answer == "" {
			return nil, fmt.Errorf("ask.user: interactive channel returned an empty answer")
		}
		return map[string]interface{}{"answer": answer}, nil
	}
}

func mockAskUser() HandlerFunc {
	return func(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		question, _ := params["question"].(string)
		if question == "" {
			return nil, fmt.Errorf("ask.user: missing required param 'question'")
		}
		// The mock behaves like a cooperative human: answer with the first
		// option when one is offered, otherwise acknowledge the question.
		if options := stringSlice(params["options"]); len(options) > 0 {
			return map[string]interface{}{"answer": options[0]}, nil
		}
		return map[string]interface{}{"answer": "mock-answer"}, nil
	}
}

// stringSlice normalises an array param that may hold any element types.
func stringSlice(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		if typed, ok := v.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
