package harness

import (
	"sort"

	"github.com/castwell/forge/internal/agent/core"
)

// jsonSchemaFor converts a tool's parameter declarations to the JSON Schema
// object the tools protocol wants.
//
// This is B5's fix in one function: the registry has always known each
// parameter's type, description and required-ness — it just never left the
// process. The model finally sees fields as fields instead of a prose list of
// parameter names.
//
// Required marks come from BOTH declarations the schema carries (per-param
// Required and the tool-level RequiredParams list); a union is the only
// reading that cannot drop a requirement — the two lists have always been
// allowed to disagree, and dropping either side's answer would silently
// weaken the contract.
func jsonSchemaFor(def *core.ToolDef) map[string]any {
	properties := make(map[string]any, len(def.InputSchema))
	requiredSet := make(map[string]bool, len(def.InputSchema)+len(def.RequiredParams))

	for name, p := range def.InputSchema {
		properties[name] = map[string]any{
			"type": paramJSONType(p.Type),
		}
		if p.Description != "" {
			properties[name].(map[string]any)["description"] = p.Description
		}
		if p.Required {
			requiredSet[name] = true
		}
	}
	for _, name := range def.RequiredParams {
		requiredSet[name] = true
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(requiredSet) > 0 {
		required := make([]string, 0, len(requiredSet))
		for name := range requiredSet {
			required = append(required, name)
		}
		// Sorted: the wire body must be deterministic for tests and caching.
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// paramJSONType maps the registry's parameter vocabulary onto JSON Schema's.
// The vocabularies already match by design; unknown spellings fall back to
// "string" rather than emitting a type the provider would reject.
func paramJSONType(t string) string {
	switch t {
	case "string", "integer", "number", "boolean", "array", "object":
		return t
	case "int", "int64":
		return "integer"
	case "float", "double":
		return "number"
	default:
		return "string"
	}
}
