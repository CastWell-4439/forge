package core

// AgentConfig holds configuration for the Agent system.
type AgentConfig struct {
	MaxSteps    int     `json:"max_steps"`
	MaxRetries  int     `json:"max_retries"`
	ModelName   string  `json:"model_name"`
	Temperature float64 `json:"temperature"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() AgentConfig {
	return AgentConfig{
		MaxSteps:    20,
		MaxRetries:  3,
		ModelName:   "claude-opus-4-6-v1",
		Temperature: 0.7,
	}
}
