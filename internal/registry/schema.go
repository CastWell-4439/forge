// Package registry implements the Workflow YAML schema, parser, template engine,
// DAG compiler, and hot-reloading registry for Forge v2 automation workflows.
package registry

import "time"

// Workflow represents the top-level YAML schema for a Forge workflow definition.
// Maps to: apiVersion/kind/metadata/triggers/config/inputs/stages
type Workflow struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   Metadata       `yaml:"metadata"`
	Triggers   []TriggerDef   `yaml:"triggers"`
	Config     WorkflowConfig `yaml:"config"`
	Inputs     map[string]string `yaml:"inputs"`
	Stages     []Stage        `yaml:"stages"`
}

// Metadata contains workflow identification.
type Metadata struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

// TriggerDef defines how a workflow is triggered.
type TriggerDef struct {
	Type     string `yaml:"type"`     // "poll", "webhook", "cron", "manual"
	Source   string `yaml:"source"`   // e.g. "feishu_mcp"
	Interval string `yaml:"interval"` // e.g. "2m"
	Query    string `yaml:"query"`    // filter expression
	DedupKey string `yaml:"dedup_key"` // template for deduplication
}

// WorkflowConfig holds execution-level configuration.
type WorkflowConfig struct {
	Timeout    string     `yaml:"timeout"`
	MaxRetries int        `yaml:"max_retries"`
	HITL       HITLConfig `yaml:"hitl"`
}

// HITLConfig defines human-in-the-loop auto-pause conditions.
type HITLConfig struct {
	AutoPauseOn []string `yaml:"auto_pause_on"`
}

// Stage represents a pipeline stage containing one or more tasks.
type Stage struct {
	Name     string    `yaml:"name"`
	Parallel bool      `yaml:"parallel"`
	Tasks    []TaskDef `yaml:"tasks"`
}

// TaskDef defines a single task within a stage.
type TaskDef struct {
	Worker    string            `yaml:"worker"`    // worker type: "ai", "git", "mcp", "shell", etc.
	Action    string            `yaml:"action"`    // action to perform
	Params    map[string]any    `yaml:"params"`    // action parameters (may contain templates)
	Output    string            `yaml:"output"`    // variable name for task result
	Condition string            `yaml:"condition"` // CEL expression; task runs only if true
	Timeout   string            `yaml:"timeout"`   // task-level timeout override
	Retry     *TaskRetryConfig  `yaml:"retry"`     // task-level retry override
}

// TaskRetryConfig defines per-task retry settings.
type TaskRetryConfig struct {
	MaxAttempts int    `yaml:"max_attempts"`
	Backoff     string `yaml:"backoff"` // "fixed", "exponential"
	Interval    string `yaml:"interval"`
}

// --- Compiled representations (post-parse) ---

// CompiledWorkflow is the validated, ready-to-execute form of a Workflow.
type CompiledWorkflow struct {
	Name        string
	Version     string
	Description string
	DAG         *DAGGraph // compiled execution graph
	Triggers    []CompiledTrigger
	Config      CompiledConfig
	Inputs      map[string]string
	Stages      []CompiledStage
}

// CompiledTrigger is the parsed trigger with duration values.
type CompiledTrigger struct {
	Type     string
	Source   string
	Interval time.Duration
	Query    string
	DedupKey string
}

// CompiledConfig holds parsed config with actual durations.
type CompiledConfig struct {
	Timeout    time.Duration
	MaxRetries int
	HITL       HITLConfig
}

// CompiledStage is a validated stage ready for DAG compilation.
type CompiledStage struct {
	Name     string
	Parallel bool
	Tasks    []CompiledTask
}

// CompiledTask is a validated task with parsed timeout/retry.
type CompiledTask struct {
	Worker    string
	Action    string
	Params    map[string]any
	Output    string
	Condition string
	Timeout   time.Duration
	Retry     *CompiledRetry
}

// CompiledRetry holds parsed retry configuration.
type CompiledRetry struct {
	MaxAttempts int
	Backoff     string
	Interval    time.Duration
}
