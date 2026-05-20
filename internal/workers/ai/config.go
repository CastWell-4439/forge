// Package ai implements the AI Worker for Forge workflows.
// It wraps the Layer 1 Agent Session (harness.AgentLoop) to provide
// workflow-level LLM actions: analyze, synthesize, classify, summarize, generate_code_plan.
package ai

// ModelConfig maps action names to specific model/temperature settings.
type ModelConfig struct {
	Model       string  // e.g. "claude-sonnet-4-20250514", "gpt-4o"
	Temperature float64 // 0.0 - 1.0
	MaxTokens   int     // max output tokens
}

// Config holds the AI Worker configuration.
type Config struct {
	// DefaultModel is used when no action-specific override exists.
	DefaultModel ModelConfig

	// ActionModels maps action name → model config override.
	// Keys: "analyze", "synthesize", "classify", "summarize", "generate_code_plan"
	ActionModels map[string]ModelConfig

	// MaxSteps controls the ReAct loop iteration limit.
	MaxSteps int

	// MaxContextTokens is the context window budget.
	MaxContextTokens int
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		DefaultModel: ModelConfig{
			Model:       "claude-sonnet-4-20250514",
			Temperature: 0.3,
			MaxTokens:   4096,
		},
		ActionModels: map[string]ModelConfig{
			"analyze": {
				Model:       "claude-sonnet-4-20250514",
				Temperature: 0.2,
				MaxTokens:   8192,
			},
			"generate_code_plan": {
				Model:       "claude-sonnet-4-20250514",
				Temperature: 0.1,
				MaxTokens:   8192,
			},
			"classify": {
				Model:       "claude-sonnet-4-20250514",
				Temperature: 0.0,
				MaxTokens:   1024,
			},
		},
		MaxSteps:         10,
		MaxContextTokens: 100000,
	}
}

// ModelForAction returns the model config for a given action, falling back to default.
func (c *Config) ModelForAction(action string) ModelConfig {
	if m, ok := c.ActionModels[action]; ok {
		return m
	}
	return c.DefaultModel
}
