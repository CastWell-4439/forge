package agent

import (
	"testing"

	"github.com/castwell/forge/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractYAML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		isEmpty  bool
	}{
		{
			name:     "plain YAML",
			input:    "name: test\ntasks:\n  t1:\n    handler: media.download",
			contains: "name: test",
		},
		{
			name:     "markdown yaml fence",
			input:    "```yaml\nname: test\ntasks:\n  t1:\n    handler: media.download\n```",
			contains: "name: test",
		},
		{
			name:     "markdown generic fence",
			input:    "```\nname: test\ntasks:\n  t1:\n    handler: media.download\n```",
			contains: "name: test",
		},
		{
			name:     "leading text",
			input:    "Here is the DAG:\nname: test\ntasks:\n  t1:\n    handler: media.download",
			contains: "name: test",
		},
		{
			name:     "tabs to spaces",
			input:    "name: test\ntasks:\n\tt1:\n\t\thandler: media.download",
			contains: "name: test",
		},
		{
			name:    "empty string",
			input:   "",
			isEmpty: true,
		},
		{
			name:    "whitespace only",
			input:   "   \n\n  ",
			isEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractYAML(tc.input)
			if tc.isEmpty {
				assert.Empty(t, result)
			} else {
				assert.Contains(t, result, tc.contains)
				assert.NotContains(t, result, "```")
			}
		})
	}
}

func TestValidateSchemaValid(t *testing.T) {
	yamlStr := `name: test-dag
tasks:
  download:
    handler: media.download
    params:
      url: "https://example.com/video.mp4"
  encode:
    handler: video.encode
    params:
      resolution: "1080p"
    depends_on:
      - download`

	dag, issues := validateSchema(yamlStr)
	require.NotNil(t, dag)
	assert.Empty(t, issues)
	assert.Equal(t, "test-dag", dag.Name)
	assert.Len(t, dag.Tasks, 2)
}

func TestValidateSchemaMissingName(t *testing.T) {
	yamlStr := `tasks:
  download:
    handler: media.download`

	dag, issues := validateSchema(yamlStr)
	assert.Nil(t, dag)
	assert.True(t, len(issues) > 0)
	assert.Equal(t, "L2", issues[0].Level)
}

func TestValidateSchemaNoTasks(t *testing.T) {
	yamlStr := `name: empty-dag`

	dag, issues := validateSchema(yamlStr)
	assert.Nil(t, dag)
	require.True(t, len(issues) > 0)
	hasTaskError := false
	for _, issue := range issues {
		if issue.Level == "L2" && issue.Severity == SeverityError {
			hasTaskError = true
		}
	}
	assert.True(t, hasTaskError)
}

func TestValidateSchemaMissingHandler(t *testing.T) {
	yamlStr := `name: bad-dag
tasks:
  download:
    params:
      url: "test"`

	dag, issues := validateSchema(yamlStr)
	assert.Nil(t, dag)
	require.True(t, len(issues) > 0)
	assert.Equal(t, "L2", issues[0].Level)
	assert.Contains(t, issues[0].Message, "missing handler")
}

func TestDAGValidatorFullPipeline(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	yamlStr := `name: valid-pipeline
tasks:
  download:
    handler: media.download
    params:
      url: "https://example.com/video.mp4"
    timeout: 60s
  probe:
    handler: video.probe
    params:
      video_path: "/tmp/video.mp4"
    depends_on:
      - download
  encode:
    handler: video.encode
    params:
      video_path: "/tmp/video.mp4"
    depends_on:
      - probe
  upload:
    handler: media.upload
    params:
      file_path: "/tmp/output.mp4"
    depends_on:
      - encode`

	result := validator.Validate(yamlStr)
	assert.True(t, result.Valid, "expected valid but got issues: %v", result.Issues)
	require.NotNil(t, result.DAG)
	assert.Equal(t, "valid-pipeline", result.DAG.Name)
}

func TestDAGValidatorL3UnknownHandler(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	yamlStr := `name: bad-handler
tasks:
  download:
    handler: media.download
    params:
      url: "test"
  magic:
    handler: ai.magic_transform
    params: {}
    depends_on:
      - download`

	result := validator.Validate(yamlStr)
	assert.False(t, result.Valid)

	hasL3Error := false
	for _, issue := range result.Issues {
		if issue.Level == "L3" && issue.Severity == SeverityError {
			hasL3Error = true
			assert.Contains(t, issue.Message, "unknown handler")
			assert.Contains(t, issue.Message, "ai.magic_transform")
		}
	}
	assert.True(t, hasL3Error)
}

func TestDAGValidatorL3CycleDetection(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	yamlStr := `name: cycle-dag
tasks:
  a:
    handler: media.download
    params:
      url: "test"
    depends_on:
      - c
  b:
    handler: video.probe
    params: {}
    depends_on:
      - a
  c:
    handler: video.encode
    params:
      resolution: "1080p"
    depends_on:
      - b`

	result := validator.Validate(yamlStr)
	assert.False(t, result.Valid)

	hasCycleError := false
	for _, issue := range result.Issues {
		if issue.Level == "L3" {
			hasCycleError = true
		}
	}
	assert.True(t, hasCycleError)
}

func TestDAGValidatorL4MissingRequiredParam(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	// media.download requires "url" param.
	yamlStr := `name: missing-param
tasks:
  download:
    handler: media.download
    params: {}`

	result := validator.Validate(yamlStr)
	assert.False(t, result.Valid)

	hasL4Error := false
	for _, issue := range result.Issues {
		if issue.Level == "L4" && issue.Severity == SeverityError {
			hasL4Error = true
			assert.Contains(t, issue.Message, "required param")
		}
	}
	assert.True(t, hasL4Error)
}

func TestDAGValidatorL4UnknownParam(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	yamlStr := `name: unknown-param
tasks:
  download:
    handler: media.download
    params:
      url: "https://example.com/video.mp4"
      nonexistent_param: "value"`

	result := validator.Validate(yamlStr)
	// Unknown params are warnings, not errors.
	hasWarning := false
	for _, issue := range result.Issues {
		if issue.Level == "L4" && issue.Severity == SeverityWarning {
			hasWarning = true
		}
	}
	assert.True(t, hasWarning)
	// Should still be valid (warnings only).
	assert.True(t, result.Valid)
}

func TestDAGValidatorMarkdownWrapped(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	yamlStr := "```yaml\nname: wrapped\ntasks:\n  download:\n    handler: media.download\n    params:\n      url: \"test\"\n```"

	result := validator.Validate(yamlStr)
	assert.True(t, result.Valid, "issues: %v", result.Issues)
	require.NotNil(t, result.DAG)
	assert.Equal(t, "wrapped", result.DAG.Name)
}

func TestValidateRaw(t *testing.T) {
	registry := tools.DefaultRegistry()
	validator := NewDAGValidator(registry)

	validYAML := `name: test
tasks:
  download:
    handler: media.download
    params:
      url: "test"`

	dag, err := validator.ValidateRaw(validYAML)
	require.NoError(t, err)
	assert.Equal(t, "test", dag.Name)

	// Invalid YAML.
	_, err = validator.ValidateRaw("not yaml at all: {{{")
	assert.Error(t, err)
}

func TestValidationResultErrorSummary(t *testing.T) {
	r := &ValidationResult{
		Issues: []ValidationIssue{
			{Level: "L2", Severity: SeverityError, Message: "missing name"},
			{Level: "L4", Severity: SeverityWarning, Message: "unknown param"},
			{Level: "L3", Severity: SeverityError, Message: "unknown handler"},
		},
	}

	summary := r.ErrorSummary()
	assert.Contains(t, summary, "missing name")
	assert.Contains(t, summary, "unknown handler")
	assert.NotContains(t, summary, "unknown param") // warnings excluded
}

func TestValidationResultHasErrors(t *testing.T) {
	noErrors := &ValidationResult{
		Issues: []ValidationIssue{
			{Level: "L4", Severity: SeverityWarning, Message: "minor"},
		},
	}
	assert.False(t, noErrors.HasErrors())

	withErrors := &ValidationResult{
		Issues: []ValidationIssue{
			{Level: "L2", Severity: SeverityError, Message: "bad"},
		},
	}
	assert.True(t, withErrors.HasErrors())
}

func TestQuickSchemaCheck(t *testing.T) {
	assert.NoError(t, quickSchemaCheck("name: test\ntasks:\n  t1:\n    handler: x"))
	assert.Error(t, quickSchemaCheck("not: valid"))
	assert.Error(t, quickSchemaCheck("{{{invalid yaml"))
}
