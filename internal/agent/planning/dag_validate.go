package planning

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/castwell/forge/internal/coordinator"
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
	Level    string // "L1", "L2", "L3", "L4"
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
//
// L3 and L4 check against the WORKFLOW handler catalog, not the agent's tool
// registry. The two are different vocabularies: a tool is something the agent
// calls mid-run, a handler is something the workflow dispatches to a worker.
// Validating one against the other is how a generated DAG naming `web.fetch`
// passed every layer and then had no worker to run it — the check was looking at
// a registry that happened to contain the name, in a system that would never
// route to it.
type DAGValidator struct {
	catalog *HandlerCatalog
}

// NewDAGValidator creates a validator over a handler catalog. A nil catalog
// rejects every handler, which is the safe direction: a validator that cannot
// tell what exists must not approve what it cannot check.
func NewDAGValidator(catalog *HandlerCatalog) *DAGValidator {
	return &DAGValidator{catalog: catalog}
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

	// L2: Schema validation —parse and check required fields.
	dag, issues := validateSchema(cleanYAML)
	result.Issues = append(result.Issues, issues...)
	if dag == nil {
		result.Valid = false
		return result
	}
	result.DAG = dag

	// L3: Semantic validation —handlers exist, no cycles, deps exist.
	l3Issues := v.validateSemantic(dag)
	result.Issues = append(result.Issues, l3Issues...)

	// L4: Parameter validation —params match InputSchema.
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

	// Every handler must be one this deployment can actually dispatch to.
	for name, task := range dag.Tasks {
		if !v.catalog.Has(task.Handler) {
			msg := fmt.Sprintf("task %q uses unknown handler %q", name, task.Handler)
			if suggestion := v.catalog.FindSimilar(task.Handler); suggestion != "" {
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

// validateParams performs L4 validation: checks a task's params against what its
// handler requires.
//
// The executor gets its own rule rather than the generic required-param check,
// because its params carry prose: "task is present" is not enough — an empty
// string satisfies presence and gives the executor nothing to do. Catching that
// here costs one validation; catching it at run time costs a dispatched task
// that cannot start.
func (v *DAGValidator) validateParams(dag *coordinator.DAG) []ValidationIssue {
	var issues []ValidationIssue

	for name, task := range dag.Tasks {
		spec, ok := v.catalog.Spec(task.Handler)
		if !ok {
			// Unknown handler —already reported in L3. Adding an L4 issue for a
			// handler the catalog cannot describe would be noise on top of a
			// real error.
			continue
		}

		if task.Handler == agentHandler {
			issues = append(issues, validateAgentParams(name, task.Params)...)
		}

		// Required params, as declared by the handler.
		for _, reqParam := range spec.Required {
			if _, ok := task.Params[reqParam]; !ok {
				issues = append(issues, ValidationIssue{
					Level:    "L4",
					Severity: SeverityError,
					Message:  fmt.Sprintf("task %q missing required param %q for handler %q", name, reqParam, task.Handler),
				})
			}
		}
	}

	return issues
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
