package git

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ProjectConfig holds the configuration for a project's git operations.
type ProjectConfig struct {
	Name        string `yaml:"name"`
	LocalPath   string `yaml:"local_path"`
	RemoteURL   string `yaml:"remote_url"`
	MainBranch  string `yaml:"main_branch"`
	TestTarget  string `yaml:"test_target"`  // branch for MRs (e.g. "dev-offline")
	GitLabURL   string `yaml:"gitlab_url"`
	GitLabToken string `yaml:"gitlab_token"`
	ProjectID   string `yaml:"project_id"` // GitLab project ID (URL-encoded)
}

// LoadProjectConfig loads a project configuration from a YAML file.
func LoadProjectConfig(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load project config: %w", err)
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	if cfg.LocalPath == "" {
		return nil, fmt.Errorf("project config: local_path is required")
	}
	if cfg.MainBranch == "" {
		cfg.MainBranch = "develop"
	}
	if cfg.TestTarget == "" {
		cfg.TestTarget = "dev-offline"
	}
	return &cfg, nil
}
