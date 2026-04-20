// Package structured implements M8: Structured Output — JSON Schema generation,
// response types, and validation for constraining LLM output format.
package structured

import (
	"fmt"
	"reflect"
	"strings"
)

// SchemaType represents a JSON Schema type.
type SchemaType string

const (
	SchemaString  SchemaType = "string"
	SchemaNumber  SchemaType = "number"
	SchemaInteger SchemaType = "integer"
	SchemaBoolean SchemaType = "boolean"
	SchemaObject  SchemaType = "object"
	SchemaArray   SchemaType = "array"
)

// Schema represents a JSON Schema object.
type Schema struct {
	Type        SchemaType         `json:"type"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	AnyOf       []*Schema          `json:"anyOf,omitempty"`
}

// GenerateSchema generates a JSON Schema from a Go struct using reflection.
// It reads `json` tags for field names and `desc` tags for descriptions.
//
// Example:
//
//	type AgentResponse struct {
//	    Thought string `json:"thought" desc:"The agent's reasoning"`
//	    Action  *ToolCall `json:"action,omitempty" desc:"Tool to invoke"`
//	}
//
// produces a valid JSON Schema with properties, required fields, and descriptions.
func GenerateSchema(v interface{}) *Schema {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return generateSchemaForType(t)
}

func generateSchemaForType(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: SchemaString}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &Schema{Type: SchemaInteger}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: SchemaNumber}
	case reflect.Bool:
		return &Schema{Type: SchemaBoolean}
	case reflect.Slice:
		return &Schema{
			Type:  SchemaArray,
			Items: generateSchemaForType(t.Elem()),
		}
	case reflect.Map:
		return &Schema{
			Type: SchemaObject,
		}
	case reflect.Struct:
		return generateStructSchema(t)
	case reflect.Ptr:
		return generateSchemaForType(t.Elem())
	default:
		return &Schema{Type: SchemaString}
	}
}

func generateStructSchema(t reflect.Type) *Schema {
	schema := &Schema{
		Type:       SchemaObject,
		Properties: make(map[string]*Schema),
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		// Parse json tag.
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		name, opts := parseJSONTag(jsonTag)
		if name == "" {
			name = field.Name
		}

		// Generate schema for the field type.
		fieldSchema := generateSchemaForType(field.Type)

		// Add description from `desc` tag.
		if desc := field.Tag.Get("desc"); desc != "" {
			fieldSchema.Description = desc
		}

		// Add enum from `enum` tag.
		if enumTag := field.Tag.Get("enum"); enumTag != "" {
			fieldSchema.Enum = strings.Split(enumTag, ",")
		}

		schema.Properties[name] = fieldSchema

		// Field is required unless it has `omitempty` or is a pointer.
		if !opts.Contains("omitempty") && field.Type.Kind() != reflect.Ptr {
			schema.Required = append(schema.Required, name)
		}
	}

	return schema
}

// parseJSONTag splits a json tag into the field name and options.
func parseJSONTag(tag string) (string, tagOptions) {
	if idx := strings.Index(tag, ","); idx != -1 {
		return tag[:idx], tagOptions(tag[idx+1:])
	}
	return tag, ""
}

// tagOptions is a comma-separated list of options in a json tag.
type tagOptions string

// Contains checks if an option is present in the tag.
func (o tagOptions) Contains(opt string) bool {
	for _, s := range strings.Split(string(o), ",") {
		if s == opt {
			return true
		}
	}
	return false
}

// FormatForLLM returns the schema as a compact description string suitable
// for including in LLM system prompts (when the API doesn't support
// response_format natively).
func FormatForLLM(s *Schema) string {
	if s == nil {
		return "{}"
	}
	return formatSchema(s, 0)
}

func formatSchema(s *Schema, indent int) string {
	prefix := strings.Repeat("  ", indent)

	switch s.Type {
	case SchemaObject:
		if len(s.Properties) == 0 {
			return "object"
		}
		var lines []string
		lines = append(lines, "{")
		for name, prop := range s.Properties {
			required := ""
			for _, r := range s.Required {
				if r == name {
					required = " (required)"
					break
				}
			}
			desc := ""
			if prop.Description != "" {
				desc = fmt.Sprintf(" // %s", prop.Description)
			}
			lines = append(lines, fmt.Sprintf("%s  %q: %s%s%s",
				prefix, name, formatSchema(prop, indent+1), required, desc))
		}
		lines = append(lines, prefix+"}")
		return strings.Join(lines, "\n")
	case SchemaArray:
		if s.Items != nil {
			return fmt.Sprintf("[%s]", formatSchema(s.Items, indent))
		}
		return "[]"
	default:
		result := string(s.Type)
		if len(s.Enum) > 0 {
			result += fmt.Sprintf(" enum(%s)", strings.Join(s.Enum, "|"))
		}
		return result
	}
}
