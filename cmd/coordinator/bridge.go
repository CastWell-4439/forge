package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/registry"
)

// bridgeDAG converts a registry-compiled workflow — the stages/worker/action
// dialect that is the YAML contract with the external orchestrator — into the
// coordinator's execution DAG. Workflow validation (④) lives on the registry
// side and dispatch (⑤) on the coordinator side; this bridge is the seam
// where the two dialects finally meet.
//
// Mapping rules:
//   - node ID ("stage.0") becomes the coordinator task name (unique, stable)
//   - worker → handler; action travels in params["action"] (the workflow
//     workers read action from the task params)
//   - node output / condition / timeout / retry carry over one-to-one
//   - graph edges become depends_on (To depends on From)
func bridgeDAG(cw *registry.CompiledWorkflow) (*coordinator.DAG, error) {
	if cw == nil {
		return nil, fmt.Errorf("bridge: nil workflow")
	}
	g := cw.DAG
	if g == nil {
		compiled, err := registry.CompileDAG(cw)
		if err != nil {
			return nil, fmt.Errorf("bridge: compile workflow %q: %w", cw.Name, err)
		}
		g = compiled
	}

	dag := &coordinator.DAG{
		Name:    cw.Name,
		Version: bridgeVersion(cw.Version),
		Tasks:   make(map[string]*coordinator.TaskDef, len(g.Nodes)),
		Edges:   make(map[string][]string, len(g.Nodes)),
	}

	for _, node := range g.Nodes {
		params := make(map[string]any, len(node.Params)+1)
		for k, v := range node.Params {
			params[k] = v
		}
		params["action"] = node.Action

		task := &coordinator.TaskDef{
			Name:      node.ID,
			Handler:   node.Worker,
			Params:    params,
			DependsOn: []string{},
			Output:    node.Output,
			Condition: node.Condition,
			Timeout:   node.Timeout,
		}
		if node.Retry != nil {
			task.Retry = coordinator.RetryPolicy{
				MaxAttempts:     node.Retry.MaxAttempts,
				InitialInterval: node.Retry.Interval,
				BackoffType:     bridgeBackoff(node.Retry.Backoff),
				Multiplier:      2.0,
			}
		}
		dag.Tasks[node.ID] = task
		dag.Edges[node.ID] = task.DependsOn
	}

	for _, edge := range g.Edges {
		task, ok := dag.Tasks[edge.To]
		if !ok {
			return nil, fmt.Errorf("bridge: workflow %q: edge references unknown task %q", cw.Name, edge.To)
		}
		task.DependsOn = append(task.DependsOn, edge.From)
	}
	for name, task := range dag.Tasks {
		dag.Edges[name] = task.DependsOn
	}

	if err := dag.Validate(); err != nil {
		return nil, fmt.Errorf("bridge: workflow %q fails coordinator validation: %w", cw.Name, err)
	}
	return dag, nil
}

// bridgeVersion parses the major number of a semantic version string
// ("1.0" → 1). Unparseable versions default to 0, the same value the YAML
// path leaves when no version is declared.
func bridgeVersion(v string) int {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

// bridgeBackoff maps registry backoff names onto coordinator backoff types.
// An unset name means fixed, matching the parser's own default.
func bridgeBackoff(name string) coordinator.BackoffType {
	switch name {
	case "exponential":
		return coordinator.BackoffExponential
	case "exponential_with_jitter":
		return coordinator.BackoffExponentialWithJitter
	case "":
		return coordinator.BackoffFixed
	default:
		return coordinator.BackoffType(name)
	}
}

// registryParamRenderer adapts the registry template engine to the
// coordinator's dispatch-time rendering seam. Dependency outputs sit at the
// top level ({{.bug_info}}) next to inputs ({{.inputs.x}}), which is how
// workflow YAML references them.
//
// A template that is exactly one placeholder passes the value through
// structurally: text/template would stringify a map as "map[file:auth.go]",
// mangling objects a workflow hands to the next task. Only templates with
// surrounding text go through string rendering.
func registryParamRenderer() coordinator.ParamRenderer {
	return func(params map[string]any, inputs, outputs map[string]any) (map[string]any, error) {
		ctx := registry.TemplateContext{}
		for name, value := range outputs {
			ctx[name] = value
		}
		ctx["inputs"] = inputs
		rendered, err := renderStructural(params, ctx)
		if err != nil {
			return nil, err
		}
		out, ok := rendered.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("rendered params are %T, want map", rendered)
		}
		return out, nil
	}
}

// renderStructural walks a params tree: exact single-placeholder strings
// resolve to the raw value; everything else renders as text via the
// registry engine (same semantics as before for mixed strings).
func renderStructural(value any, ctx registry.TemplateContext) (any, error) {
	switch v := value.(type) {
	case string:
		if path, ok := exactPlaceholder(v); ok {
			if resolved, found := lookupPath(ctx, path); found {
				return resolved, nil
			}
			// Missing path: fall back to the engine for missingkey=zero.
		}
		return registry.RenderString(v, ctx)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			rendered, err := renderStructural(item, ctx)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderStructural(item, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return value, nil
	}
}

// exactPlaceholder reports whether s is exactly "{{.a.b}}" and returns the
// dotted path ("a.b").
func exactPlaceholder(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{{.") || !strings.HasSuffix(trimmed, "}}") {
		return "", false
	}
	path := strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{."), "}}")
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "{{") || strings.Contains(path, " ") {
		return "", false
	}
	return path, true
}

// lookupPath walks a dotted path through nested maps.
func lookupPath(ctx registry.TemplateContext, path string) (any, bool) {
	var current any = map[string]any(ctx)
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
