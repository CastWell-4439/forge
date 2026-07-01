package reliability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RunResult records one repeated run outcome.
type RunResult struct {
	Index      int       `json:"index" yaml:"index"`
	RunID      string    `json:"run_id" yaml:"run_id"`
	RunDir     string    `json:"run_dir" yaml:"run_dir"`
	EvalStatus string    `json:"eval_status" yaml:"eval_status"`
	Error      string    `json:"error,omitempty" yaml:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at" yaml:"created_at"`
}

// RepeatResult summarizes repeated eval outcomes for one case.
type RepeatResult struct {
	CaseID             string         `json:"case_id" yaml:"case_id"`
	SuiteID            string         `json:"suite_id" yaml:"suite_id"`
	Total              int            `json:"total" yaml:"total"`
	Passed             int            `json:"passed" yaml:"passed"`
	Failed             int            `json:"failed" yaml:"failed"`
	PassAtK            bool           `json:"pass_at_k" yaml:"pass_at_k"`
	PassAll            bool           `json:"pass_all" yaml:"pass_all"`
	FlakyRate          float64        `json:"flaky_rate" yaml:"flaky_rate"`
	StatusDistribution map[string]int `json:"status_distribution" yaml:"status_distribution"`
	Runs               []RunResult    `json:"runs" yaml:"runs"`
	CreatedAt          time.Time      `json:"created_at" yaml:"created_at"`
}

// Summarize derives reliability metrics from repeated run outcomes.
func Summarize(caseID string, suiteID string, runs []RunResult) RepeatResult {
	result := RepeatResult{
		CaseID:             caseID,
		SuiteID:            suiteID,
		Total:              len(runs),
		PassAll:            len(runs) > 0,
		StatusDistribution: make(map[string]int),
		Runs:               runs,
		CreatedAt:          time.Now().UTC(),
	}
	for _, run := range runs {
		status := run.EvalStatus
		if status == "" {
			status = "error"
		}
		result.StatusDistribution[status]++
		if status == "passed" && run.Error == "" {
			result.Passed++
			result.PassAtK = true
			continue
		}
		result.Failed++
		result.PassAll = false
	}
	if result.Total > 0 {
		result.FlakyRate = float64(result.Failed) / float64(result.Total)
	}
	return result
}

// Write writes repeat_result.json under root.
func Write(root string, result RepeatResult) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(filepath.Join(root, "repeat_result.json"), encoded, 0o644)
}
