// Package coordinator implements the core coordination logic for Forge,
// including DAG workflow parsing, validation, and execution orchestration.
package coordinator

import (
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// BackoffType defines the backoff strategy for task retries.
type BackoffType string

const (
	// BackoffFixed uses a constant interval between retries.
	BackoffFixed BackoffType = "fixed"
	// BackoffExponential uses exponentially increasing intervals.
	BackoffExponential BackoffType = "exponential"
	// BackoffExponentialWithJitter adds random jitter to exponential backoff.
	BackoffExponentialWithJitter BackoffType = "exponential_with_jitter"
)

// FailureAction defines the behavior when a task fails.
type FailureAction string

const (
	// FailureActionFailWorkflow fails the entire workflow.
	FailureActionFailWorkflow FailureAction = "FAIL_WORKFLOW"
	// FailureActionContinue skips the failed task and continues.
	FailureActionContinue FailureAction = "CONTINUE"
	// FailureActionCompensate triggers saga compensation.
	FailureActionCompensate FailureAction = "COMPENSATE"
)

// DAG represents a Directed Acyclic Graph workflow definition.
type DAG struct {
	Name    string              `yaml:"name"`
	Version int                 `yaml:"version"`
	Timeout time.Duration       `yaml:"-"`
	Tasks   map[string]*TaskDef `yaml:"tasks"`
	Edges   map[string][]string `yaml:"-"` // task -> dependencies
}

// rawDAG is the YAML-friendly representation used during parsing.
type rawDAG struct {
	Name    string                `yaml:"name"`
	Version int                   `yaml:"version"`
	Timeout string                `yaml:"timeout"`
	Tasks   map[string]*rawTaskDef `yaml:"tasks"`
}

// TaskDef defines a single task within a DAG.
type TaskDef struct {
	Name       string                 `yaml:"-"`
	Handler    string                 `yaml:"handler"`
	Params     map[string]interface{} `yaml:"params"`
	DependsOn  []string               `yaml:"depends_on"`
	Timeout    time.Duration          `yaml:"-"`
	Retry      RetryPolicy            `yaml:"-"`
	OnFailure  FailureAction          `yaml:"on_failure"`
	Compensate string                 `yaml:"compensate"` // Saga: handler to call for rollback
}

// rawTaskDef is the YAML-friendly representation for task definitions.
type rawTaskDef struct {
	Handler    string                 `yaml:"handler"`
	Params     map[string]interface{} `yaml:"params"`
	DependsOn  []string               `yaml:"depends_on"`
	Timeout    string                 `yaml:"timeout"`
	Retry      *rawRetryPolicy        `yaml:"retry"`
	OnFailure  FailureAction          `yaml:"on_failure"`
	Compensate string                 `yaml:"compensate"`
}

// RetryPolicy defines the retry behavior for a task.
type RetryPolicy struct {
	MaxAttempts     int           `yaml:"-"`
	InitialInterval time.Duration `yaml:"-"`
	MaxInterval     time.Duration `yaml:"-"`
	BackoffType     BackoffType   `yaml:"-"`
	Multiplier      float64       `yaml:"-"`
}

// rawRetryPolicy is the YAML-friendly representation for retry policies.
type rawRetryPolicy struct {
	MaxAttempts     int     `yaml:"max_attempts"`
	Backoff         string  `yaml:"backoff"`
	InitialInterval string  `yaml:"initial_interval"`
	MaxInterval     string  `yaml:"max_interval"`
	Multiplier      float64 `yaml:"multiplier"`
}

// ParseDAG parses a YAML byte slice into a DAG structure.
func ParseDAG(data []byte) (*DAG, error) {
	var raw rawDAG
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse DAG YAML: %w", err)
	}

	if raw.Name == "" {
		return nil, fmt.Errorf("parse DAG: name is required")
	}

	dag := &DAG{
		Name:    raw.Name,
		Version: raw.Version,
		Tasks:   make(map[string]*TaskDef),
		Edges:   make(map[string][]string),
	}

	if raw.Timeout != "" {
		d, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return nil, fmt.Errorf("parse DAG timeout %q: %w", raw.Timeout, err)
		}
		dag.Timeout = d
	}

	for name, rawTask := range raw.Tasks {
		taskDef := &TaskDef{
			Name:       name,
			Handler:    rawTask.Handler,
			Params:     rawTask.Params,
			DependsOn:  rawTask.DependsOn,
			OnFailure:  rawTask.OnFailure,
			Compensate: rawTask.Compensate,
		}

		if rawTask.Timeout != "" {
			d, err := time.ParseDuration(rawTask.Timeout)
			if err != nil {
				return nil, fmt.Errorf("parse task %s timeout %q: %w", name, rawTask.Timeout, err)
			}
			taskDef.Timeout = d
		}

		if rawTask.Retry != nil {
			taskDef.Retry = RetryPolicy{
				MaxAttempts: rawTask.Retry.MaxAttempts,
				Multiplier:  rawTask.Retry.Multiplier,
			}
			switch rawTask.Retry.Backoff {
			case "fixed":
				taskDef.Retry.BackoffType = BackoffFixed
			case "exponential":
				taskDef.Retry.BackoffType = BackoffExponential
			case "exponential_with_jitter":
				taskDef.Retry.BackoffType = BackoffExponentialWithJitter
			default:
				if rawTask.Retry.Backoff != "" {
					taskDef.Retry.BackoffType = BackoffType(rawTask.Retry.Backoff)
				}
			}
			if rawTask.Retry.InitialInterval != "" {
				d, err := time.ParseDuration(rawTask.Retry.InitialInterval)
				if err != nil {
					return nil, fmt.Errorf("parse task %s retry initial_interval %q: %w", name, rawTask.Retry.InitialInterval, err)
				}
				taskDef.Retry.InitialInterval = d
			}
			if rawTask.Retry.MaxInterval != "" {
				d, err := time.ParseDuration(rawTask.Retry.MaxInterval)
				if err != nil {
					return nil, fmt.Errorf("parse task %s retry max_interval %q: %w", name, rawTask.Retry.MaxInterval, err)
				}
				taskDef.Retry.MaxInterval = d
			}
			if taskDef.Retry.Multiplier == 0 {
				taskDef.Retry.Multiplier = 2.0
			}
		}

		dag.Tasks[name] = taskDef
		dag.Edges[name] = rawTask.DependsOn
	}

	return dag, nil
}

// Validate checks the DAG for structural correctness.
// It detects cycles, orphan nodes, and timeout sanity issues.
func (d *DAG) Validate() error {
	if err := d.detectOrphans(); err != nil {
		return err
	}
	if err := d.detectCycle(); err != nil {
		return err
	}
	if err := d.checkTimeouts(); err != nil {
		return err
	}
	return nil
}

// TopologicalSort returns task names in topological order using Kahn's algorithm.
// Returns an error if the DAG contains a cycle.
func (d *DAG) TopologicalSort() ([]string, error) {
	inDegree := make(map[string]int)
	for name := range d.Tasks {
		inDegree[name] = 0
	}
	for name, task := range d.Tasks {
		for _, dep := range task.DependsOn {
			_ = dep
			inDegree[name]++
		}
	}

	// Seed the queue with nodes that have no dependencies.
	// Sort for deterministic output.
	queue := make([]string, 0)
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)

	var sorted []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		sorted = append(sorted, node)

		// Find all tasks that depend on node and decrement their in-degree.
		var newReady []string
		for name, task := range d.Tasks {
			for _, dep := range task.DependsOn {
				if dep == node {
					inDegree[name]--
					if inDegree[name] == 0 {
						newReady = append(newReady, name)
					}
				}
			}
		}
		sort.Strings(newReady)
		queue = append(queue, newReady...)
	}

	if len(sorted) != len(d.Tasks) {
		return nil, fmt.Errorf("DAG contains cycle")
	}
	return sorted, nil
}

// detectCycle uses Kahn's algorithm to check for cycles.
func (d *DAG) detectCycle() error {
	_, err := d.TopologicalSort()
	return err
}

// detectOrphans checks that all dependencies reference existing tasks.
func (d *DAG) detectOrphans() error {
	for name, task := range d.Tasks {
		for _, dep := range task.DependsOn {
			if _, ok := d.Tasks[dep]; !ok {
				return fmt.Errorf("task %q depends on %q which does not exist (orphan reference)", name, dep)
			}
		}
	}
	return nil
}

// checkTimeouts validates that task timeouts are less than the workflow timeout.
func (d *DAG) checkTimeouts() error {
	if d.Timeout == 0 {
		return nil // no workflow-level timeout set, skip check
	}
	for name, task := range d.Tasks {
		if task.Timeout > 0 && task.Timeout > d.Timeout {
			return fmt.Errorf("task %q timeout (%s) exceeds workflow timeout (%s)", name, task.Timeout, d.Timeout)
		}
	}
	return nil
}
