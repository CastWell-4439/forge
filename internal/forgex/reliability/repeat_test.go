package reliability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSummarizeAllPassed(t *testing.T) {
	result := Summarize("generic-contract-success", "generic_contract_happy_v1", []RunResult{
		{Index: 1, RunID: "run_1", EvalStatus: "passed"},
		{Index: 2, RunID: "run_2", EvalStatus: "passed"},
	})
	if result.Total != 2 || result.Passed != 2 || result.Failed != 0 {
		t.Fatalf("counts = %+v", result)
	}
	if !result.PassAtK || !result.PassAll || result.FlakyRate != 0 {
		t.Fatalf("summary = %+v", result)
	}
	if result.StatusDistribution["passed"] != 2 {
		t.Fatalf("status distribution = %+v", result.StatusDistribution)
	}
}

func TestSummarizeMixedResults(t *testing.T) {
	result := Summarize("generic-contract-success", "generic_contract_happy_v1", []RunResult{
		{Index: 1, RunID: "run_1", EvalStatus: "passed"},
		{Index: 2, RunID: "run_2", EvalStatus: "failed"},
		{Index: 3, Error: "boom"},
	})
	if result.Total != 3 || result.Passed != 1 || result.Failed != 2 {
		t.Fatalf("counts = %+v", result)
	}
	if !result.PassAtK || result.PassAll {
		t.Fatalf("pass flags = pass_at_k:%t pass_all:%t", result.PassAtK, result.PassAll)
	}
	if result.FlakyRate != float64(2)/float64(3) {
		t.Fatalf("flaky rate = %v", result.FlakyRate)
	}
	if result.StatusDistribution["passed"] != 1 || result.StatusDistribution["failed"] != 1 || result.StatusDistribution["error"] != 1 {
		t.Fatalf("status distribution = %+v", result.StatusDistribution)
	}
}

func TestWriteRepeatResult(t *testing.T) {
	dir := t.TempDir()
	result := Summarize("generic-contract-success", "generic_contract_happy_v1", []RunResult{{Index: 1, RunID: "run_1", EvalStatus: "passed"}})
	if err := Write(dir, result); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "repeat_result.json"))
	if err != nil {
		t.Fatalf("read repeat result: %v", err)
	}
	var decoded RepeatResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode repeat result: %v", err)
	}
	if decoded.CaseID != "generic-contract-success" || decoded.Total != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
}
