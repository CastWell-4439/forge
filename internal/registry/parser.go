package registry

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Parse parses raw YAML bytes into a Workflow struct and validates required fields.
func Parse(data []byte) (*Workflow, error) {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("registry: parse YAML: %w", err)
	}
	if err := validate(&wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

// Compile parses and compiles a Workflow into its ready-to-execute form.
func Compile(data []byte) (*CompiledWorkflow, error) {
	wf, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return compileWorkflow(wf)
}

// validate checks that required fields are present and semantically correct.
func validate(wf *Workflow) error {
	if wf.APIVersion == "" {
		return fmt.Errorf("registry: apiVersion is required")
	}
	if wf.Kind != "Workflow" {
		return fmt.Errorf("registry: kind must be 'Workflow', got %q", wf.Kind)
	}
	if wf.Metadata.Name == "" {
		return fmt.Errorf("registry: metadata.name is required")
	}
	if len(wf.Stages) == 0 {
		return fmt.Errorf("registry: at least one stage is required")
	}
	for i, stage := range wf.Stages {
		if stage.Name == "" {
			return fmt.Errorf("registry: stages[%d].name is required", i)
		}
		if len(stage.Tasks) == 0 {
			return fmt.Errorf("registry: stages[%d] (%s) must have at least one task", i, stage.Name)
		}
		for j, task := range stage.Tasks {
			if task.Worker == "" {
				return fmt.Errorf("registry: stages[%d].tasks[%d].worker is required", i, j)
			}
			if task.Action == "" {
				return fmt.Errorf("registry: stages[%d].tasks[%d].action is required", i, j)
			}
		}
	}
	// Check for duplicate stage names
	seen := make(map[string]bool, len(wf.Stages))
	for _, stage := range wf.Stages {
		if seen[stage.Name] {
			return fmt.Errorf("registry: duplicate stage name %q", stage.Name)
		}
		seen[stage.Name] = true
	}
	return nil
}

// compileWorkflow transforms a raw Workflow into a CompiledWorkflow.
func compileWorkflow(wf *Workflow) (*CompiledWorkflow, error) {
	cw := &CompiledWorkflow{
		Name:        wf.Metadata.Name,
		Version:     wf.Metadata.Version,
		Description: wf.Metadata.Description,
		Inputs:      wf.Inputs,
	}

	// Compile triggers
	for i, t := range wf.Triggers {
		ct, err := compileTrigger(t)
		if err != nil {
			return nil, fmt.Errorf("registry: triggers[%d]: %w", i, err)
		}
		cw.Triggers = append(cw.Triggers, ct)
	}

	// Compile config
	cfg, err := compileConfig(wf.Config)
	if err != nil {
		return nil, fmt.Errorf("registry: config: %w", err)
	}
	cw.Config = cfg

	// Compile stages
	for i, s := range wf.Stages {
		cs, err := compileStage(s)
		if err != nil {
			return nil, fmt.Errorf("registry: stages[%d] (%s): %w", i, s.Name, err)
		}
		cw.Stages = append(cw.Stages, cs)
	}

	// Compile DAG from stages
	dag, err := CompileDAG(cw)
	if err != nil {
		return nil, fmt.Errorf("registry: compile DAG: %w", err)
	}
	cw.DAG = dag

	return cw, nil
}

func compileTrigger(t TriggerDef) (CompiledTrigger, error) {
	ct := CompiledTrigger{
		Type:     t.Type,
		Source:   t.Source,
		Query:    t.Query,
		DedupKey: t.DedupKey,
	}
	if t.Interval != "" {
		d, err := time.ParseDuration(t.Interval)
		if err != nil {
			return ct, fmt.Errorf("invalid interval %q: %w", t.Interval, err)
		}
		ct.Interval = d
	}
	return ct, nil
}

func compileConfig(cfg WorkflowConfig) (CompiledConfig, error) {
	cc := CompiledConfig{
		MaxRetries: cfg.MaxRetries,
		HITL:       cfg.HITL,
	}
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return cc, fmt.Errorf("invalid timeout %q: %w", cfg.Timeout, err)
		}
		cc.Timeout = d
	}
	return cc, nil
}

func compileStage(s Stage) (CompiledStage, error) {
	cs := CompiledStage{
		Name:     s.Name,
		Parallel: s.Parallel,
	}
	for i, t := range s.Tasks {
		ct, err := compileTask(t)
		if err != nil {
			return cs, fmt.Errorf("tasks[%d]: %w", i, err)
		}
		cs.Tasks = append(cs.Tasks, ct)
	}
	return cs, nil
}

func compileTask(t TaskDef) (CompiledTask, error) {
	ct := CompiledTask{
		Worker:    t.Worker,
		Action:    t.Action,
		Params:    t.Params,
		Output:    t.Output,
		Condition: t.Condition,
	}
	if t.Timeout != "" {
		d, err := time.ParseDuration(t.Timeout)
		if err != nil {
			return ct, fmt.Errorf("invalid timeout %q: %w", t.Timeout, err)
		}
		ct.Timeout = d
	}
	if t.Retry != nil {
		cr, err := compileRetry(t.Retry)
		if err != nil {
			return ct, err
		}
		ct.Retry = cr
	}
	return ct, nil
}

func compileRetry(r *TaskRetryConfig) (*CompiledRetry, error) {
	cr := &CompiledRetry{
		MaxAttempts: r.MaxAttempts,
		Backoff:     r.Backoff,
	}
	if r.Interval != "" {
		d, err := time.ParseDuration(r.Interval)
		if err != nil {
			return nil, fmt.Errorf("invalid retry interval %q: %w", r.Interval, err)
		}
		cr.Interval = d
	}
	return cr, nil
}
