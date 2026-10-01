package cases

import (
	"fmt"
	"strconv"
	"strings"
)

// Actual is what a run actually produced, expressed in the same vocabulary a
// case declares its expectations in. The caller derives it from run artifacts,
// which keeps this package free of any dependency on the run model.
type Actual struct {
	Status           string
	FinalDecision    string
	Errors           int
	Lessons          int
	ValidationFailed int
	ArtifactsMissing int
}

// FieldVerdict is one declared expectation compared against what happened.
type FieldVerdict struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// AtLeast marks a *_min expectation: the actual value only has to reach the
	// declared one, not equal it.
	AtLeast bool `json:"at_least,omitempty"`
	Passed  bool `json:"passed"`
}

// Verdict is the result of replaying one case against its declared outcome.
type Verdict struct {
	CaseID string         `json:"case_id"`
	Passed bool           `json:"passed"`
	Fields []FieldVerdict `json:"fields"`
}

// Failed returns the expectations that did not hold.
func (v Verdict) Failed() []FieldVerdict {
	var failed []FieldVerdict
	for _, field := range v.Fields {
		if !field.Passed {
			failed = append(failed, field)
		}
	}
	return failed
}

// Compare checks a run against the outcome its case declares.
//
// Every case file has always carried an "expected" block, but nothing read it
// beyond printing it, so a case could not fail. Each declared field is compared
// on its own, so a regression reports which expectation moved rather than only
// that something did.
//
// A field the case does not declare is not checked: a nil pointer means
// "unspecified", and an empty string means the same for the string fields.
func Compare(spec CaseSpec, actual Actual) Verdict {
	verdict := Verdict{CaseID: spec.ID, Passed: true}
	expected := spec.Expected

	record := func(field, want, got string, atLeast, passed bool) {
		if !passed {
			verdict.Passed = false
		}
		verdict.Fields = append(verdict.Fields, FieldVerdict{
			Field:    field,
			Expected: want,
			Actual:   got,
			AtLeast:  atLeast,
			Passed:   passed,
		})
	}

	if expected.Status != "" {
		record("status", expected.Status, actual.Status, false, expected.Status == actual.Status)
	}
	if expected.FinalDecision != "" {
		record("final_decision", expected.FinalDecision, actual.FinalDecision, false,
			expected.FinalDecision == actual.FinalDecision)
	}

	exact := func(field string, want *int, got int) {
		if want == nil {
			return
		}
		record(field, strconv.Itoa(*want), strconv.Itoa(got), false, *want == got)
	}
	atLeast := func(field string, want *int, got int) {
		if want == nil {
			return
		}
		// Expected stays a bare number so the recorded verdict is machine
		// readable; AtLeast carries the comparison, and rendering adds the ">=".
		record(field, strconv.Itoa(*want), strconv.Itoa(got), true, got >= *want)
	}

	exact("errors", expected.Errors, actual.Errors)
	exact("lessons", expected.Lessons, actual.Lessons)
	atLeast("lessons_min", expected.LessonsMin, actual.Lessons)
	exact("validation_failed", expected.ValidationFailed, actual.ValidationFailed)
	atLeast("validation_failed_min", expected.ValidationFailedMin, actual.ValidationFailed)
	exact("artifacts_missing", expected.ArtifactsMissing, actual.ArtifactsMissing)
	atLeast("artifacts_missing_min", expected.ArtifactsMissingMin, actual.ArtifactsMissing)

	return verdict
}

// Summary renders the verdict as one line for command output.
func (v Verdict) Summary() string {
	if v.Passed {
		return fmt.Sprintf("expected outcome matched (%d fields checked)", len(v.Fields))
	}
	failed := v.Failed()
	parts := make([]string, 0, len(failed))
	for _, field := range failed {
		parts = append(parts, fmt.Sprintf("%s: expected %s, got %s",
			field.Field, Describe(field.Expected, field.AtLeast), field.Actual))
	}
	return fmt.Sprintf("expected outcome NOT matched (%d/%d fields): %s",
		len(failed), len(v.Fields), strings.Join(parts, "; "))
}

// Describe renders an expectation for humans, showing the comparison a minimum
// stands for. The stored value stays a bare number.
func Describe(expected string, atLeast bool) string {
	if atLeast {
		return ">= " + expected
	}
	return expected
}
