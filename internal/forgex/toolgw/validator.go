package toolgw

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

const (
	ValidatorRequiredInputsPresent  = "required_inputs_present"
	ValidatorRequiredOutputsPresent = "required_outputs_present"
	ValidatorRequiredAssetsNotEmpty = "required_assets_not_empty"
	ValidatorPromptNotEmpty         = "prompt_not_empty"
	ValidatorMaterialIDsNotEmpty    = "material_ids_not_empty"
)

var validationSeq atomic.Uint64

// ValidateInputs validates tool args against a contract.
func ValidateInputs(runID string, contract ToolContract, args map[string]any) []ValidationResult {
	var results []ValidationResult
	for _, validator := range contract.Validators {
		switch strings.TrimSpace(validator) {
		case ValidatorRequiredInputsPresent:
			results = append(results, validateRequired(runID, contract.Name, ValidatorRequiredInputsPresent, contract.RequiredInputs, args)...)
		case ValidatorRequiredAssetsNotEmpty:
			results = append(results, validateNotEmpty(runID, contract.Name, ValidatorRequiredAssetsNotEmpty, args, "required_assets"))
		case ValidatorPromptNotEmpty:
			results = append(results, validateNotEmpty(runID, contract.Name, ValidatorPromptNotEmpty, args, "prompt"))
		case ValidatorMaterialIDsNotEmpty:
			results = append(results, validateNotEmpty(runID, contract.Name, ValidatorMaterialIDsNotEmpty, args, "material_ids"))
		case "", ValidatorRequiredOutputsPresent:
			// Output-only validator or empty config; ignore during input validation.
		default:
			results = append(results, newValidationResult(runID, contract.Name, validator, ValidationFailed, fmt.Sprintf("unknown input validator %q", validator)))
		}
	}
	return results
}

// ValidateOutputs validates tool outputs against a contract.
func ValidateOutputs(runID string, contract ToolContract, output map[string]any) []ValidationResult {
	var results []ValidationResult
	for _, validator := range contract.Validators {
		switch strings.TrimSpace(validator) {
		case ValidatorRequiredOutputsPresent:
			results = append(results, validateRequired(runID, contract.Name, ValidatorRequiredOutputsPresent, contract.RequiredOutputs, output)...)
		case "", ValidatorRequiredInputsPresent, ValidatorRequiredAssetsNotEmpty, ValidatorPromptNotEmpty, ValidatorMaterialIDsNotEmpty:
			// Input-only validator or empty config; ignore during output validation.
		default:
			results = append(results, newValidationResult(runID, contract.Name, validator, ValidationFailed, fmt.Sprintf("unknown output validator %q", validator)))
		}
	}
	return results
}

func validateRequired(runID, toolName, validator string, keys []string, values map[string]any) []ValidationResult {
	results := make([]ValidationResult, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value, ok := values[key]
		if !ok || isEmpty(value) {
			results = append(results, newValidationResult(runID, toolName, validator, ValidationFailed, fmt.Sprintf("required field %q is missing or empty", key)))
			continue
		}
		results = append(results, newValidationResult(runID, toolName, validator, ValidationPassed, fmt.Sprintf("required field %q is present", key)))
	}
	return results
}

func validateNotEmpty(runID, toolName, validator string, values map[string]any, key string) ValidationResult {
	value, ok := values[key]
	if !ok || isEmpty(value) {
		return newValidationResult(runID, toolName, validator, ValidationFailed, fmt.Sprintf("%s must be non-empty", key))
	}
	return newValidationResult(runID, toolName, validator, ValidationPassed, fmt.Sprintf("%s is non-empty", key))
}

func newValidationResult(runID, toolName, validator string, status ValidationStatus, message string) ValidationResult {
	seq := validationSeq.Add(1)
	return ValidationResult{
		ID:        fmt.Sprintf("validation-%s-%d", fallback(runID, "run"), seq),
		RunID:     runID,
		ToolName:  toolName,
		Status:    status,
		Validator: strings.TrimSpace(validator),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}

func isEmpty(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case []string:
		return len(v) == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}
