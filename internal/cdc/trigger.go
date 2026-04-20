package cdc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// TriggerConfig defines a CDC trigger from YAML configuration.
type TriggerConfig struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`  // "cdc"
	Source        TriggerSource     `yaml:"source"`
	Workflow      string            `yaml:"workflow"`
	ParamsMapping map[string]string `yaml:"params_mapping"`
}

// TriggerSource defines the CDC source in a trigger configuration.
type TriggerSource struct {
	Type    string   `yaml:"type"`   // "postgres", "redis", etc.
	Table   string   `yaml:"table"`
	Events  []string `yaml:"events"` // ["INSERT", "UPDATE", "DELETE"]
	Filter  string   `yaml:"filter"` // SQL-like filter
	Pattern string   `yaml:"pattern"` // Redis key pattern
}

// TriggerSet is a collection of trigger configurations parsed from YAML.
type TriggerSet struct {
	Triggers []TriggerConfig `yaml:"triggers"`
}

// ParseTriggerConfig parses a YAML trigger configuration.
func ParseTriggerConfig(yamlData []byte) (*TriggerSet, error) {
	var ts TriggerSet
	if err := yaml.Unmarshal(yamlData, &ts); err != nil {
		return nil, fmt.Errorf("parse trigger config: %w", err)
	}
	return &ts, nil
}

// WorkflowSubmitter is called when a CDC event triggers a workflow.
type WorkflowSubmitter func(ctx context.Context, workflowName string, params map[string]interface{}) error

// TriggerManager manages CDC triggers and routes events to workflows.
type TriggerManager struct {
	triggers  []TriggerConfig
	sources   map[string]Source
	submitter WorkflowSubmitter
	mu        sync.RWMutex
	stopCh    chan struct{}
}

// NewTriggerManager creates a new TriggerManager.
func NewTriggerManager(submitter WorkflowSubmitter) *TriggerManager {
	return &TriggerManager{
		sources:   make(map[string]Source),
		submitter: submitter,
		stopCh:    make(chan struct{}),
	}
}

// LoadConfig loads trigger configurations and sets up CDC sources.
func (m *TriggerManager) LoadConfig(ts *TriggerSet, sourceFactory func(TriggerSource) (Source, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range ts.Triggers {
		if tc.Type != "cdc" {
			continue
		}

		source, err := sourceFactory(tc.Source)
		if err != nil {
			return fmt.Errorf("create source for trigger %q: %w", tc.Name, err)
		}

		m.triggers = append(m.triggers, tc)
		m.sources[tc.Name] = source
	}

	return nil
}

// Start begins listening on all configured CDC sources.
func (m *TriggerManager) Start(ctx context.Context) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, tc := range m.triggers {
		source, ok := m.sources[tc.Name]
		if !ok {
			continue
		}
		go m.runTrigger(ctx, tc, source)
	}
}

// Stop stops all CDC triggers.
func (m *TriggerManager) Stop() {
	close(m.stopCh)

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, source := range m.sources {
		if err := source.Close(); err != nil {
			log.Printf("WARN: cdc trigger: close source: %v", err)
		}
	}
}

func (m *TriggerManager) runTrigger(ctx context.Context, tc TriggerConfig, source Source) {
	handler := func(event Event) {
		// Build workflow params from the event using params_mapping.
		params := m.buildParams(tc.ParamsMapping, event)

		log.Printf("INFO: cdc trigger %q: %s on %s → workflow %q",
			tc.Name, event.Operation, event.Table, tc.Workflow)

		if err := m.submitter(ctx, tc.Workflow, params); err != nil {
			log.Printf("ERROR: cdc trigger %q: submit workflow: %v", tc.Name, err)
		}
	}

	if err := source.Subscribe(ctx, handler); err != nil {
		log.Printf("ERROR: cdc trigger %q: subscribe failed: %v", tc.Name, err)
	}
}

// buildParams maps event data to workflow parameters using the params_mapping.
// Supports "{{.new.field}}" and "{{.old.field}}" template syntax.
func (m *TriggerManager) buildParams(mapping map[string]string, event Event) map[string]interface{} {
	params := make(map[string]interface{})

	for paramName, template := range mapping {
		value := resolveTemplate(template, event)
		params[paramName] = value
	}

	return params
}

// resolveTemplate resolves a "{{.new.field}}" or "{{.old.field}}" template.
func resolveTemplate(template string, event Event) interface{} {
	template = strings.TrimSpace(template)

	// Strip {{ and }}
	if strings.HasPrefix(template, "{{") && strings.HasSuffix(template, "}}") {
		template = strings.TrimPrefix(template, "{{")
		template = strings.TrimSuffix(template, "}}")
		template = strings.TrimSpace(template)
	}

	// Parse ".new.field" or ".old.field"
	if strings.HasPrefix(template, ".new.") {
		field := template[5:]
		if event.NewData != nil {
			if v, ok := event.NewData[field]; ok {
				return v
			}
		}
	}
	if strings.HasPrefix(template, ".old.") {
		field := template[5:]
		if event.OldData != nil {
			if v, ok := event.OldData[field]; ok {
				return v
			}
		}
	}

	// Return template as-is if not resolvable.
	return template
}

// TriggerCount returns the number of configured triggers.
func (m *TriggerManager) TriggerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.triggers)
}
