package registry

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// TemplateContext holds the runtime context for template rendering.
// Keys include: "event", "inputs", "project", and any task outputs by name.
type TemplateContext map[string]any

// RenderString renders a single template string against the provided context.
// Template syntax: {{.key}} or {{.nested.field}}
// Non-template strings are returned unchanged.
func RenderString(tmpl string, ctx TemplateContext) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	t, err := template.New("").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("registry/template: parse %q: %w", tmpl, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("registry/template: execute %q: %w", tmpl, err)
	}
	return buf.String(), nil
}

// RenderParams recursively renders all template strings within a params map.
// Supports nested maps and string slices.
func RenderParams(params map[string]any, ctx TemplateContext) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}
	result := make(map[string]any, len(params))
	for k, v := range params {
		rendered, err := renderValue(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}

// renderValue recursively renders template values in arbitrary structures.
func renderValue(v any, ctx TemplateContext) (any, error) {
	switch val := v.(type) {
	case string:
		return RenderString(val, ctx)
	case map[string]any:
		return RenderParams(val, ctx)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			r, err := renderValue(item, ctx)
			if err != nil {
				return nil, err
			}
			result[i] = r
		}
		return result, nil
	default:
		// Non-string scalars (int, float, bool, nil) pass through unchanged
		return v, nil
	}
}

// RenderInputs renders the workflow inputs map against the event context.
func RenderInputs(inputs map[string]string, ctx TemplateContext) (map[string]any, error) {
	result := make(map[string]any, len(inputs))
	for k, v := range inputs {
		rendered, err := RenderString(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}
