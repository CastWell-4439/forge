package planning

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/castwell/forge/internal/agent/workers"
	"github.com/castwell/forge/internal/coordinator"
	"gopkg.in/yaml.v3"
)

// ValidationSeverity indicates the severity of a validation issue.
type ValidationSeverity string

const (
	// SeverityError means the DAG cannot be used.
	SeverityError ValidationSeverity = "error"
	// SeverityWarning means the DAG is usable but may have issues.
	SeverityWarning ValidationSeverity = "warning"
)

// ValidationIssue represents a single validation problem found in a DAG.
type ValidationIssue struct {
	Level    string             // "L1", "L2", "L3", "L4"
	Severity ValidationSeverity
	Message  string
}

// Error implements the error interface for ValidationIssue.
func (v ValidationIssue) Error() string {
	return fmt.Sprintf("[%s/%s] %s", v.Level, v.Severity, v.Message)
}

// ValidationResult holds the outcome of DAG validation.
type ValidationResult struct {
	Valid  bool
	Issues []ValidationIssue
	DAG    *coordinator.DAG // non-nil if at least L2 passed
}

// HasErrors returns true if there are any error-severity issues.
func (r *ValidationResult) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ErrorSummary returns a human-readable summary of all errors for LLM retry prompts.
func (r *ValidationResult) ErrorSummary() string {
	var errs []string
	for _, issue := range r.Issues {
		if issue.Severity == SeverityError {
			errs = append(errs, issue.Error())
		}
	}
	return strings.Join(errs, "\n")
}

// DAGValidator performs the 4-layer validation pipeline on DAG YAML.
// L1: Format extraction, L2: Schema validation, L3: Semantic validation,
// L4: Parameter validation. From agent-tech-spec 3.3.1.
type DAGValidator struct {
	registry *workers.ToolRegistry
}

// NewDAGValidator creates a new DAGValidator.
func NewDAGValidator(registry *workers.ToolRegistry) *DAGValidator {
	return &DAGValidator{registry: registry}
}

// Validate runs the full 4-layer validation pipeline.
func (v *DAGValidator) Validate(rawYAML string) *ValidationResult {
	result := &ValidationResult{Valid: true}

	// L1: Extract and clean YAML.
	cleanYAML := extractYAML(rawYAML)
	if cleanYAML == "" {
		result.Valid = false
		result.Issues = append(result.Issues, ValidationIssue{
			Level:    "L1",
			Severity: SeverityError,
			Message:  "no valid YAML content found",
		})
		return result
	}

	// L2: Schema validation �?parse and check required fields.
	dag, issues := validateSchema(cleanYAML)
	result.Issues = append(result.Issues, issues...)
	if dag == nil {
		result.Valid = false
		return result
	}
	result.DAG = dag

	// L3: Semantic validation �?handlers exist, no cycles, deps exist.
	l3Issues := v.validateSemantic(dag)
	result.Issues = append(result.Issues, l3Issues...)

	// L4: Parameter validation �?params match InputSchema.
	l4Issues := v.validateParams(dag)
	result.Issues = append(result.Issues, l4Issues...)

	result.Valid = !result.HasErrors()
	return result
}

// extractYAML performs L1 cleanup: strips markdown fences, tabs, and
// extracts the YAML content from LLM output.
func extractYAML(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// Strip markdown code fences.
	fencePattern := regexp.MustCompile("(?s)^```(?:ya?ml)?\\s*\\n(.+?)\\n?```\\s*$")
	if m := fencePattern.FindStringSubmatch(s); len(m) > 1 {
		s = m[1]
	}

	// Remove any leading/trailing non-YAML text by finding the first "name:" line.
	lines := strings.Split(s, "\n")
	startIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "name:") || strings.HasPrefix(trimmed, "tasks:") {
			startIdx = i
			break
		}
	}
	if startIdx > 0 {
		s = strings.Join(lines[startIdx:], "\n")
	}

	// Fix common issues: tabs to 2 spaces.
	s = strings.ReplaceAll(s, "\t", "  ")

	return strings.TrimSpace(s)
}

// validateSchema performs L2 validation: parses YAML and checks required fields.
func validateSchema(yamlStr string) (*coordinator.DAG, []ValidationIssue) {
	var issues []ValidationIssue

	dag, err := coordinator.ParseDAG([]byte(yamlStr))
	if err != nil {
		issues = append(issues, ValidationIssue{
			Level:    "L2",
			Severity: SeverityError,
			Message:  fmt.Sprintf("YAML parse error: %v", err),
		})
		return nil, issues
	}

	if dag.Name == "" {
		issues = append(issues, ValidationIssue{
			Level:    "L2",
			Severity: SeverityError,
			Message:  "DAG name is required",
		})
	}

	if len(dag.Tasks) == 0 {
		issues = append(issues, ValidationIssue{
			Level:    "L2",
			Severity: SeverityError,
			Message:  "DAG must have at least one task",
		})
		return nil, issues
	}

	// Check every task has a handler.
	for name, task := range dag.Tasks {
		if task.Handler == "" {
			issues = append(issues, ValidationIssue{
				Level:    "L2",
				Severity: SeverityError,
				Message:  fmt.Sprintf("task %q missing handler field", name),
			})
		}
	}

	if hasErrorSeverity(issues) {
		return nil, issues
	}
	return dag, issues
}

// validateSemantic performs L3 validation: handler existence, cycle detection,
// dependency existence checks.
func (v *DAGValidator) validateSemantic(dag *coordinator.DAG) []ValidationIssue {
	var issues []ValidationIssue

	// Check all handlers exist in the tool registry.
	for name, task := range dag.Tasks {
		if !v.registry.HasHandler(task.Handler) {
			suggestion := v.registry.FindSimilar(task.Handler)
			msg := fmt.Sprintf("task %q uses unknown handler %q", name, task.Handler)
			if suggestion != "" {
				msg += fmt.Sprintf(", did you mean %q?", suggestion)
			}
			issues = append(issues, ValidationIssue{
				Level:    "L3",
				Severity: SeverityError,
				Message:  msg,
			})
		}
	}

	// Cycle detection and structural validation via coordinator.DAG.Validate().
	if err := dag.Validate(); err != nil {
		issues = append(issues, ValidationIssue{
			Level:    "L3",
			Severity: SeverityError,
			Message:  fmt.Sprintf("DAG structural error: %v", err),
		})
	}

	return issues
}

// validateParams performs L4 validation: checks task params against tool InputSchema.
func (v *DAGValidator) validateParams(dag *coordinator.DAG) []ValidationIssue {
	var issues []ValidationIssue

	for name, task := range dag.Tasks {
		toolDef := v.registry.GetTool(task.Handler)
		if toolDef == nil {
			// Handler doesn't exist �?already reported in L3.
			continue
		}

		// Check required parameters are present.
		for _, reqParam := range toolDef.RequiredParams {
			if task.Params == nil {
				issues = append(issues, ValidationIssue{
					Level:    "L4",
					Severity: SeverityError,
					Message:  fmt.Sprintf("task %q missing required param %q for handler %q", name, reqParam, task.Handler),
				})
				continue
			}
			if _, ok := task.Params[reqParam]; !ok {
				issues = append(issues, ValidationIssue{
					Level:    "L4",
					Severity: SeverityError,
					Message:  fmt.Sprintf("task %q missing required param %q for handler %q", name, reqParam, task.Handler),
				})
			}
		}

		// Also check InputSchema required flags.
		for paramName, paramDef := range toolDef.InputSchema {
			if !paramDef.Required {
				continue
			}
			// Skip if already checked via RequiredParams.
			if isInSlice(paramName, toolDef.RequiredParams) {
				continue
			}
			if task.Params == nil {
				issues = append(issues, ValidationIssue{
					Level:    "L4",
					Severity: SeverityError,
					Message:  fmt.Sprintf("task %q missing required param %q for handler %q", name, paramName, task.Handler),
				})
				continue
			}
			if _, ok := task.Params[paramName]; !ok {
				issues = append(issues, ValidationIssue{
					Level:    "L4",
					Severity: SeverityError,
					Message:  fmt.Sprintf("task %q missing required param %q for handler %q", name, paramName, task.Handler),
				})
			}
		}

		// Type checking: warn if param types don't match schema.
		if task.Params != nil {
			v.checkParamTypes(name, task, toolDef, &issues)
		}
	}

	return issues
}

// checkParamTypes validates parameter types against the tool's InputSchema.
func (v *DAGValidator) checkParamTypes(taskName string, task *coordinator.TaskDef, toolDef *workers.ToolDef, issues *[]ValidationIssue) {
	for paramName, paramVal := range task.Params {
		schemaDef, ok := toolDef.InputSchema[paramName]
		if !ok {
			// Unknown parameter �?warning, not error.
			*issues = append(*issues, ValidationIssue{
				Level:    "L4",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("task %q has unknown param %q for handler %q", taskName, paramName, task.Handler),
			})
			continue
		}

		if !checkType(paramVal, schemaDef.Type) {
			*issues = append(*issues, ValidationIssue{
				Level:    "L4",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("task %q param %q expected type %q", taskName, paramName, schemaDef.Type),
			})
		}
	}
}

// checkType validates that a value matches the expected JSON Schema type.
func checkType(val interface{}, expectedType string) bool {
	switch expectedType {
	case "string":
		_, ok := val.(string)
		return ok
	case "integer":
		switch v := val.(type) {
		case int, int32, int64:
			return true
		case float64:
			return v == float64(int64(v))
		default:
			return false
		}
	case "number":
		switch val.(type) {
		case int, int32, int64, float32, float64:
			return true
		default:
			return false
		}
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "array":
		return isSlice(val)
	case "object":
		_, ok := val.(map[string]interface{})
		return ok
	default:
		return true // unknown type �?allow
	}
}

// isSlice checks if a value is a slice type (from YAML arrays).
func isSlice(v interface{}) bool {
	if v == nil {
		return false
	}
	switch v.(type) {
	case []interface{}, []string, []int, []float64:
		return true
	default:
		return false
	}
}

// ValidateRaw is a convenience for validating a raw YAML string.
// Returns an error if validation fails.
func (v *DAGValidator) ValidateRaw(rawYAML string) (*coordinator.DAG, error) {
	result := v.Validate(rawYAML)
	if !result.Valid {
		return nil, fmt.Errorf("DAG validation failed:\n%s", result.ErrorSummary())
	}
	return result.DAG, nil
}

func hasErrorSeverity(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

func isInSlice(s string, ss []string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// agentDAGSchema is used for quick YAML structure check before full parse.
type agentDAGSchema struct {
	Name  string                         `yaml:"name"`
	Tasks map[string]agentTaskSchema     `yaml:"tasks"`
}

// agentTaskSchema is a lightweight task schema for validation.
type agentTaskSchema struct {
	Handler   string                 `yaml:"handler"`
	Params    map[string]interface{} `yaml:"params"`
	DependsOn []string               `yaml:"depends_on"`
}

// quickSchemaCheck does a lightweight YAML parse to check basic structure
// before passing to the full coordinator.ParseDAG.
func quickSchemaCheck(yamlStr string) error {
	var schema agentDAGSchema
	if err := yaml.Unmarshal([]byte(yamlStr), &schema); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	if schema.Name == "" {
		return fmt.Errorf("missing 'name' field")
	}
	if len(schema.Tasks) == 0 {
		return fmt.Errorf("missing 'tasks' field or empty tasks")
	}
	return nil
}
