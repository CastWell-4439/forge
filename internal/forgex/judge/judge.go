package judge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictUnknown = "unknown"
)

// Judge scores subjective or rubric-based aspects of a ForgeX run.
//
// Implementations must be explicit adapters. The default ForgeX deterministic
// eval path must not depend on any external judge.
type Judge interface {
	Score(ctx context.Context, input Input) (Result, error)
}

// Rubric describes the criteria a judge should apply.
type Rubric struct {
	ID          string      `json:"id" yaml:"id"`
	Title       string      `json:"title" yaml:"title"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Criteria    []Criterion `json:"criteria,omitempty" yaml:"criteria,omitempty"`
}

// Criterion is one rubric item.
type Criterion struct {
	ID          string  `json:"id" yaml:"id"`
	Description string  `json:"description" yaml:"description"`
	Weight      float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
}

// Input contains the minimal data needed by a judge adapter.
type Input struct {
	RunID     string         `json:"run_id" yaml:"run_id"`
	CaseID    string         `json:"case_id,omitempty" yaml:"case_id,omitempty"`
	Rubric    Rubric         `json:"rubric" yaml:"rubric"`
	Artifacts map[string]any `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Trace     []TraceItem    `json:"trace,omitempty" yaml:"trace,omitempty"`
}

// TraceItem is a compact trajectory item passed to a judge adapter.
type TraceItem struct {
	Type    string         `json:"type" yaml:"type"`
	Message string         `json:"message,omitempty" yaml:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty" yaml:"data,omitempty"`
}

// Result is a judge adapter's structured outcome.
type Result struct {
	RunID     string    `json:"run_id" yaml:"run_id"`
	RubricID  string    `json:"rubric_id" yaml:"rubric_id"`
	Score     float64   `json:"score" yaml:"score"`
	Verdict   string    `json:"verdict" yaml:"verdict"`
	Reason    string    `json:"reason,omitempty" yaml:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

// NoopJudge returns an unknown verdict without calling anything external.
type NoopJudge struct{}

func (NoopJudge) Score(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{
		RunID:     input.RunID,
		RubricID:  input.Rubric.ID,
		Score:     0,
		Verdict:   VerdictUnknown,
		Reason:    "noop judge: no external or subjective scoring configured",
		CreatedAt: time.Now().UTC(),
	}, nil
}

// MockJudge is a deterministic local implementation for tests and future
// integration wiring. It never calls external services.
type MockJudge struct {
	ScoreValue float64
	Verdict    string
	Reason     string
}

func (m MockJudge) Score(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	verdict := strings.TrimSpace(m.Verdict)
	if verdict == "" {
		verdict = VerdictUnknown
	}
	reason := strings.TrimSpace(m.Reason)
	if reason == "" {
		reason = "mock judge result"
	}
	return Result{
		RunID:     input.RunID,
		RubricID:  input.Rubric.ID,
		Score:     m.ScoreValue,
		Verdict:   verdict,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// ValidateRubric checks the minimal structure needed before passing a rubric to
// a judge adapter.
func ValidateRubric(r Rubric) error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("rubric id is required")
	}
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("rubric title is required")
	}
	seen := make(map[string]struct{}, len(r.Criteria))
	for i, c := range r.Criteria {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("criterion[%d]: id is required", i)
		}
		if _, ok := seen[c.ID]; ok {
			return fmt.Errorf("duplicate criterion id: %s", c.ID)
		}
		seen[c.ID] = struct{}{}
		if strings.TrimSpace(c.Description) == "" {
			return fmt.Errorf("criterion %s: description is required", c.ID)
		}
		if c.Weight < 0 {
			return fmt.Errorf("criterion %s: weight must be non-negative", c.ID)
		}
	}
	return nil
}
