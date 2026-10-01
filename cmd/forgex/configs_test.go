package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigAndWorkflowYAMLParses loads every shipped config and workflow file.
//
// These files are read at run time, so a syntax error surfaces as an empty or
// missing rule set rather than a clear failure. One stray ": " inside a scalar
// is enough to turn it into a nested mapping and break the whole file. Parsing
// them here keeps that cheap.
func TestConfigAndWorkflowYAMLParses(t *testing.T) {
	patterns := []string{
		filepath.Join("..", "..", "configs", "**", "*.yaml"),
		filepath.Join("..", "..", "workflows", "*.yaml"),
	}

	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		t.Fatal("no config or workflow YAML found; the check would silently pass")
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Errorf("%s does not parse: %v", path, err)
		}
	}
	t.Logf("parsed %d config/workflow files", len(paths))
}
