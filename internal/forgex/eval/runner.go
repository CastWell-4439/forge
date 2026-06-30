package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// RunArtifacts contains the minimal run data needed by eval assertions.
type RunArtifacts struct {
	Run                 model.Run                  `json:"run"`
	TaskPacket          model.TaskPacket           `json:"task_packet"`
	Events              []model.Event              `json:"events"`
	ToolCalls           []model.ToolCall           `json:"tool_calls"`
	PolicyDecisions     []model.PolicyDecision     `json:"policy_decisions"`
	ContractValidations []model.ContractValidation `json:"contract_validations"`
	WorldState          model.WorldState           `json:"world_state"`
	StateClaims         []model.StateClaim         `json:"state_claims"`
	Artifacts           []model.ArtifactRecord     `json:"artifacts"`
	Errors              []model.ErrorEnvelope      `json:"errors"`
	StopSignals         []model.StopSignalRecord   `json:"stop_signals"`
	StopDecisions       []model.StopDecision       `json:"stop_decisions"`
}

// Run evaluates one suite against a run directory and writes eval_result.json.
func Run(ctx context.Context, runDir string, rulesPath string, suiteID string) (model.EvalResult, error) {
	if err := ctx.Err(); err != nil {
		return model.EvalResult{}, err
	}
	cfg, err := LoadRules(rulesPath)
	if err != nil {
		return model.EvalResult{}, err
	}
	suite, err := cfg.FindSuite(suiteID)
	if err != nil {
		return model.EvalResult{}, err
	}
	artifacts, err := LoadRunArtifacts(runDir)
	if err != nil {
		return model.EvalResult{}, err
	}

	result := model.EvalResult{
		ID:        "eval_" + artifacts.Run.ID,
		RunID:     artifacts.Run.ID,
		SuiteID:   suite.ID,
		Status:    model.EvalPassed,
		CreatedAt: time.Now().UTC(),
	}
	for _, evalCase := range suite.Cases {
		caseResult := evaluateCase(artifacts, evalCase)
		if caseResult.Status == model.EvalFailed {
			result.Status = model.EvalFailed
		}
		result.Cases = append(result.Cases, caseResult)
	}
	if len(result.Cases) == 0 {
		result.Status = model.EvalSkipped
	}
	if err := WriteResult(runDir, result); err != nil {
		return model.EvalResult{}, err
	}
	return result, nil
}

// LoadRunArtifacts reads the standard ForgeX run directory files.
func LoadRunArtifacts(runDir string) (RunArtifacts, error) {
	var artifacts RunArtifacts
	if err := readJSON(filepath.Join(runDir, "run.json"), &artifacts.Run); err != nil {
		return RunArtifacts{}, err
	}
	if err := readYAML(filepath.Join(runDir, "task_packet.yaml"), &artifacts.TaskPacket); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "events.jsonl"), &artifacts.Events); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "tool_calls.jsonl"), &artifacts.ToolCalls); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "policy_decisions.jsonl"), &artifacts.PolicyDecisions); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "contract_validations.jsonl"), &artifacts.ContractValidations); err != nil {
		return RunArtifacts{}, err
	}
	if err := readOptionalYAML(filepath.Join(runDir, "world_state.yaml"), &artifacts.WorldState); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "state_claims.jsonl"), &artifacts.StateClaims); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "artifacts.jsonl"), &artifacts.Artifacts); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "errors.jsonl"), &artifacts.Errors); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "stop_signals.jsonl"), &artifacts.StopSignals); err != nil {
		return RunArtifacts{}, err
	}
	if err := readJSONL(filepath.Join(runDir, "stop_decisions.jsonl"), &artifacts.StopDecisions); err != nil {
		return RunArtifacts{}, err
	}
	return artifacts, nil
}

// WriteResult writes eval_result.json into the run directory.
func WriteResult(runDir string, result model.EvalResult) error {
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(filepath.Join(runDir, "eval_result.json"), encoded, 0o644)
}

func evaluateCase(artifacts RunArtifacts, evalCase Case) model.EvalCaseResult {
	caseResult := model.EvalCaseResult{ID: evalCase.ID, Status: model.EvalPassed}
	for _, assertion := range evalCase.Assertions {
		assertionResult := evaluateAssertion(artifacts, assertion)
		if assertionResult.Status == model.EvalFailed {
			caseResult.Status = model.EvalFailed
		}
		caseResult.Assertions = append(caseResult.Assertions, assertionResult)
	}
	if len(caseResult.Assertions) == 0 {
		caseResult.Status = model.EvalSkipped
	}
	return caseResult
}

func evaluateAssertion(artifacts RunArtifacts, assertion Assertion) model.EvalAssertionResult {
	actual, err := ResolvePath(artifacts, assertion.Path)
	result := model.EvalAssertionResult{
		Path:     assertion.Path,
		Op:       assertion.Op,
		Expected: assertion.Value,
		Actual:   actual,
		Status:   model.EvalPassed,
	}
	if err != nil {
		result.Status = model.EvalFailed
		result.Message = err.Error()
		return result
	}
	if !compare(actual, assertion.Op, assertion.Value) {
		result.Status = model.EvalFailed
		result.Message = fmt.Sprintf("expected %s %s %q, got %q", assertion.Path, assertion.Op, assertion.Value, actual)
	}
	return result
}

func compare(actual string, op string, expected string) bool {
	switch op {
	case "eq":
		return actual == expected
	case "ne":
		return actual != expected
	case "contains":
		return strings.Contains(actual, expected)
	default:
		return false
	}
}

// ResolvePath resolves simple paths like errors[0].category or stop_decisions[0].action.
func ResolvePath(root any, path string) (string, error) {
	value := reflect.ValueOf(root)
	for _, segment := range strings.Split(path, ".") {
		name, index, hasIndex, err := parseSegment(segment)
		if err != nil {
			return "", err
		}
		value, err = fieldByName(value, name)
		if err != nil {
			return "", err
		}
		if hasIndex {
			value = indirect(value)
			if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
				return "", fmt.Errorf("path segment %q is not indexable", segment)
			}
			if index < 0 || index >= value.Len() {
				return "", fmt.Errorf("index out of range for %q", segment)
			}
			value = value.Index(index)
		}
	}
	return stringify(value), nil
}

func parseSegment(segment string) (name string, index int, hasIndex bool, err error) {
	if segment == "" {
		return "", 0, false, fmt.Errorf("empty path segment")
	}
	open := strings.Index(segment, "[")
	if open < 0 {
		return segment, 0, false, nil
	}
	if !strings.HasSuffix(segment, "]") {
		return "", 0, false, fmt.Errorf("invalid indexed segment: %s", segment)
	}
	parsed, err := strconv.Atoi(segment[open+1 : len(segment)-1])
	if err != nil {
		return "", 0, false, err
	}
	return segment[:open], parsed, true, nil
}

func fieldByName(value reflect.Value, name string) (reflect.Value, error) {
	value = indirect(value)
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("cannot select field %q from %s", name, value.Kind())
	}
	field := value.FieldByName(toExportedName(name))
	if !field.IsValid() {
		return reflect.Value{}, fmt.Errorf("field not found: %s", name)
	}
	return field, nil
}

func toExportedName(name string) string {
	parts := strings.Split(name, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func indirect(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return value
		}
		value = value.Elem()
	}
	return value
}

func stringify(value reflect.Value) string {
	value = indirect(value)
	if !value.IsValid() {
		return ""
	}
	if value.Kind() == reflect.String {
		return value.String()
	}
	if value.CanInterface() {
		return fmt.Sprint(value.Interface())
	}
	return fmt.Sprint(value)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, target)
}

func readOptionalYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return yaml.Unmarshal(data, target)
}

func readJSONL[T any](path string, target *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return err
		}
		*target = append(*target, item)
	}
	return scanner.Err()
}
