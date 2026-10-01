package cases

import (
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

// declaredSpec mirrors the shape of a real registry entry: a mix of exact
// expectations, minimums, and fields left unspecified.
func declaredSpec() CaseSpec {
	return CaseSpec{
		ID: "case-under-test",
		Expected: ExpectedOutcome{
			Status:           "stopped",
			FinalDecision:    "stop",
			Errors:           intPtr(1),
			LessonsMin:       intPtr(1),
			ValidationFailed: intPtr(1),
		},
	}
}

func matchingActual() Actual {
	return Actual{
		Status:           "stopped",
		FinalDecision:    "stop",
		Errors:           1,
		Lessons:          3,
		ValidationFailed: 1,
		ArtifactsMissing: 7,
	}
}

func TestComparePassesWhenExpectationsHold(t *testing.T) {
	verdict := Compare(declaredSpec(), matchingActual())
	if !verdict.Passed {
		t.Fatalf("expected a pass, got: %s", verdict.Summary())
	}
	if len(verdict.Fields) != 5 {
		t.Errorf("checked %d fields, want the 5 that were declared", len(verdict.Fields))
	}
	if !strings.Contains(verdict.Summary(), "matched") {
		t.Errorf("summary should report the match: %s", verdict.Summary())
	}
}

// TestCompareIgnoresUndeclaredFields pins the meaning of an omitted expectation:
// a registry entry only asserts what it writes down.
func TestCompareIgnoresUndeclaredFields(t *testing.T) {
	actual := matchingActual()
	actual.ArtifactsMissing = 99 // declaredSpec says nothing about missing artifacts
	actual.Lessons = 5           // only a minimum is declared, so the exact count is free

	verdict := Compare(declaredSpec(), actual)
	if !verdict.Passed {
		t.Errorf("fields nobody declared must not fail a case: %s", verdict.Summary())
	}
	for _, field := range verdict.Fields {
		if field.Field == "artifacts_missing" || field.Field == "lessons" {
			t.Errorf("%s was checked even though the case does not declare it", field.Field)
		}
	}
}

func TestCompareReportsTheFieldThatMoved(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Actual)
		wantField string
	}{
		{"status", func(a *Actual) { a.Status = "succeeded" }, "status"},
		{"final decision", func(a *Actual) { a.FinalDecision = "continue" }, "final_decision"},
		{"exact count", func(a *Actual) { a.Errors = 0 }, "errors"},
		{"minimum not reached", func(a *Actual) { a.Lessons = 0 }, "lessons_min"},
		{"validation count", func(a *Actual) { a.ValidationFailed = 0 }, "validation_failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := matchingActual()
			tc.mutate(&actual)

			verdict := Compare(declaredSpec(), actual)
			if verdict.Passed {
				t.Fatal("expected a failure")
			}
			failed := verdict.Failed()
			if len(failed) != 1 || failed[0].Field != tc.wantField {
				t.Fatalf("failed fields = %+v, want exactly %q", failed, tc.wantField)
			}
			if failed[0].Expected == failed[0].Actual {
				t.Errorf("the verdict should show both values: %+v", failed[0])
			}
		})
	}
}

func TestCompareMinimumIsASatisfiedByALargerValue(t *testing.T) {
	spec := CaseSpec{ID: "minimal", Expected: ExpectedOutcome{LessonsMin: intPtr(2)}}

	verdict := Compare(spec, Actual{Lessons: 5})
	if !verdict.Passed {
		t.Errorf("a value above the minimum must pass: %s", verdict.Summary())
	}
	if len(verdict.Fields) != 1 || !verdict.Fields[0].AtLeast {
		t.Errorf("the field should be marked as a minimum: %+v", verdict.Fields)
	}
}

func TestCompareWithNoDeclaredExpectationsPasses(t *testing.T) {
	verdict := Compare(CaseSpec{ID: "empty"}, Actual{Status: "whatever"})
	if !verdict.Passed {
		t.Error("a case that declares nothing cannot fail")
	}
	if len(verdict.Fields) != 0 {
		t.Errorf("nothing should have been checked, got %+v", verdict.Fields)
	}
}

func TestVerdictSummaryNamesEveryFailure(t *testing.T) {
	actual := matchingActual()
	actual.Status = "succeeded"
	actual.Errors = 0

	summary := Compare(declaredSpec(), actual).Summary()
	for _, want := range []string{"status", "errors", "2/5"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q should mention %q", summary, want)
		}
	}
}
