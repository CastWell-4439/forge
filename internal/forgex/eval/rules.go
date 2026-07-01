package eval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level eval rule configuration.
type Config struct {
	Version int     `yaml:"version"`
	Suites  []Suite `yaml:"suites"`
}

// Suite groups related regression cases.
type Suite struct {
	ID    string `yaml:"id"`
	Cases []Case `yaml:"cases"`
}

// Case contains assertions for one badcase/regression scenario.
type Case struct {
	ID         string      `yaml:"id"`
	Assertions []Assertion `yaml:"assertions"`
	Trajectory Trajectory  `yaml:"trajectory"`
}

// Assertion compares a value at path against an expected value.
type Assertion struct {
	Path  string `yaml:"path"`
	Op    string `yaml:"op"`
	Value string `yaml:"value"`
}

// Trajectory contains process-level expectations for one run.
type Trajectory struct {
	RequiredEvents      []string `yaml:"required_events"`
	ExpectedToolCalls   []string `yaml:"expected_tool_calls"`
	ForbiddenTools      []string `yaml:"forbidden_tools"`
	MaxToolCalls        *int     `yaml:"max_tool_calls"`
	RequiredStopActions []string `yaml:"required_stop_actions"`
}

// LoadRules loads eval rules from YAML.
func LoadRules(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Version == 0 {
		return nil, fmt.Errorf("eval rules version is required")
	}
	return &cfg, nil
}

// FindSuite returns the suite with the requested id.
func (c *Config) FindSuite(id string) (Suite, error) {
	if c == nil {
		return Suite{}, fmt.Errorf("eval config is nil")
	}
	for _, suite := range c.Suites {
		if suite.ID == id {
			return suite, nil
		}
	}
	return Suite{}, fmt.Errorf("eval suite not found: %s", id)
}
