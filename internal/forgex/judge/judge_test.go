package judge

import (
	"context"
	"strings"
	"testing"
)

func TestNoopJudge(t *testing.T) {
	result, err := NoopJudge{}.Score(context.Background(), Input{
		RunID:  "run_1",
		Rubric: Rubric{ID: "rubric_1", Title: "Rubric"},
	})
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if result.RunID != "run_1" || result.RubricID != "rubric_1" {
		t.Fatalf("result ids = %+v", result)
	}
	if result.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown", result.Verdict)
	}
	if !strings.Contains(result.Reason, "noop judge") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestMockJudge(t *testing.T) {
	result, err := MockJudge{ScoreValue: 0.9, Verdict: VerdictPass, Reason: "looks good"}.Score(context.Background(), Input{
		RunID:  "run_1",
		Rubric: Rubric{ID: "rubric_1", Title: "Rubric"},
	})
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if result.Score != 0.9 || result.Verdict != VerdictPass || result.Reason != "looks good" {
		t.Fatalf("result = %+v", result)
	}
}

func TestValidateRubric(t *testing.T) {
	rubric := Rubric{
		ID:    "rubric_1",
		Title: "Rubric",
		Criteria: []Criterion{
			{ID: "correctness", Description: "Judge correctness", Weight: 1},
			{ID: "clarity", Description: "Judge clarity", Weight: 0.5},
		},
	}
	if err := ValidateRubric(rubric); err != nil {
		t.Fatalf("ValidateRubric() error = %v", err)
	}
}

func TestValidateRubricRejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		rubric Rubric
	}{
		{name: "missing id", rubric: Rubric{Title: "Rubric"}},
		{name: "missing title", rubric: Rubric{ID: "rubric"}},
		{name: "missing criterion id", rubric: Rubric{ID: "rubric", Title: "Rubric", Criteria: []Criterion{{Description: "x"}}}},
		{name: "duplicate criterion id", rubric: Rubric{ID: "rubric", Title: "Rubric", Criteria: []Criterion{{ID: "x", Description: "x"}, {ID: "x", Description: "y"}}}},
		{name: "missing description", rubric: Rubric{ID: "rubric", Title: "Rubric", Criteria: []Criterion{{ID: "x"}}}},
		{name: "negative weight", rubric: Rubric{ID: "rubric", Title: "Rubric", Criteria: []Criterion{{ID: "x", Description: "x", Weight: -1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRubric(tt.rubric); err == nil {
				t.Fatalf("ValidateRubric() expected error")
			}
		})
	}
}
