package demo_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/demo"
	"github.com/castwell/forge/internal/forgex/model"
)

func TestRunGenericContractViolationDemoWritesLessons(t *testing.T) {
	root := t.TempDir()
	taxonomy := repoPath(t, "configs/forgex/failure_taxonomy.yaml")
	policy := repoPath(t, "configs/forgex/stop_policies.yaml")
	packet := repoPath(t, "examples/forgex/task_packet_generic_contract_violation.yaml")
	contracts := repoPath(t, "configs/forgex/tool_contracts/generic_tool_contracts.yaml")
	toolPolicy := repoPath(t, "configs/forgex/policies/safe_default.yaml")

	runID, err := demo.RunGenericContractViolationDemoWithControl(context.Background(), root, taxonomy, policy, packet, contracts, toolPolicy, "")
	if err != nil {
		t.Fatalf("RunGenericContractViolationDemoWithControl() error = %v", err)
	}
	runDir := filepath.Join(root, "runs", runID)

	// A lessons.jsonl must be written with exactly one lesson.
	lessonsPath := filepath.Join(runDir, "lessons.jsonl")
	lines := readJSONLLines(t, lessonsPath)
	if len(lines) != 1 {
		t.Fatalf("lessons.jsonl line count = %d, want 1", len(lines))
	}
	var lesson model.Lesson
	if err := lines[0].decode(&lesson); err != nil {
		t.Fatalf("decode lesson: %v", err)
	}
	if lesson.SourceRunID != runID {
		t.Errorf("lesson SourceRunID = %q, want %q", lesson.SourceRunID, runID)
	}
	if lesson.Category != "tool_contract_violation" {
		t.Errorf("lesson Category = %q, want tool_contract_violation", lesson.Category)
	}
	if strings.TrimSpace(lesson.Content) == "" {
		t.Error("lesson Content is empty, want a recommendation")
	}

	// The report must surface the lesson and its recommendation.
	reportBytes, err := os.ReadFile(filepath.Join(runDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	report := string(reportBytes)
	for _, want := range []string{"## Lessons", "recommendation:", lesson.ID} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func TestRunGenericContractSuccessDemoWritesNoLessons(t *testing.T) {
	root := t.TempDir()
	taxonomy := repoPath(t, "configs/forgex/failure_taxonomy.yaml")
	policy := repoPath(t, "configs/forgex/stop_policies.yaml")
	packet := repoPath(t, "examples/forgex/task_packet_generic_contract_success.yaml")
	contracts := repoPath(t, "configs/forgex/tool_contracts/generic_tool_contracts.yaml")
	toolPolicy := repoPath(t, "configs/forgex/policies/safe_default.yaml")

	runID, err := demo.RunGenericContractSuccessDemoWithControl(context.Background(), root, taxonomy, policy, packet, contracts, toolPolicy, "")
	if err != nil {
		t.Fatalf("RunGenericContractSuccessDemoWithControl() error = %v", err)
	}
	runDir := filepath.Join(root, "runs", runID)

	// A clean run must not write a lessons file, so no misleading bad case is
	// implied.
	if _, err := os.Stat(filepath.Join(runDir, "lessons.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("lessons.jsonl should not exist for a clean run, stat err = %v", err)
	}

	// The report still renders the Lessons section with an explicit placeholder.
	reportBytes, err := os.ReadFile(filepath.Join(runDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	report := string(reportBytes)
	if !strings.Contains(report, "## Lessons") || !strings.Contains(report, "_No lessons recorded._") {
		t.Errorf("report missing empty Lessons section\n%s", report)
	}
}

// jsonlLine is one raw line of a JSONL file.
type jsonlLine []byte

func (l jsonlLine) decode(v any) error {
	return json.Unmarshal(l, v)
}

func readJSONLLines(t *testing.T, path string) []jsonlLine {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open JSONL file %s: %v", path, err)
	}
	defer file.Close()

	var lines []jsonlLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := make([]byte, len(scanner.Bytes()))
		copy(line, scanner.Bytes())
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL %s: %v", path, err)
	}
	return lines
}
