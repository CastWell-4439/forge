package cases

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cases.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return path
}

const validRegistry = `version: 1
cases:
  - id: generic-contract-violation
    description: violation path
    task_packet: examples/forgex/task_packet_generic_contract_violation.yaml
    suite: generic_contract_regression_v1
    expected:
      status: stopped
      final_decision: stop
      errors: 1
      lessons_min: 1
  - id: generic-contract-success
    task_packet: examples/forgex/task_packet_generic_contract_success.yaml
    suite: generic_contract_happy_v1
    expected:
      status: succeeded
      final_decision: continue
      errors: 0
      lessons: 0
`

func TestLoadValid(t *testing.T) {
	reg, err := Load(writeRegistry(t, validRegistry))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reg.Version != 1 {
		t.Fatalf("Version = %d, want 1", reg.Version)
	}
	if len(reg.Cases) != 2 {
		t.Fatalf("len(Cases) = %d, want 2", len(reg.Cases))
	}
	if got := reg.IDs(); len(got) != 2 || got[0] != "generic-contract-violation" || got[1] != "generic-contract-success" {
		t.Fatalf("IDs() = %v", got)
	}

	c, err := reg.Find("generic-contract-violation")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if c.Suite != "generic_contract_regression_v1" {
		t.Fatalf("Suite = %q", c.Suite)
	}
	if c.Expected.Errors == nil || *c.Expected.Errors != 1 {
		t.Fatalf("Expected.Errors = %v, want 1", c.Expected.Errors)
	}
	if c.Expected.LessonsMin == nil || *c.Expected.LessonsMin != 1 {
		t.Fatalf("Expected.LessonsMin = %v, want 1", c.Expected.LessonsMin)
	}

	success, err := reg.Find("generic-contract-success")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if success.Expected.Errors == nil || *success.Expected.Errors != 0 {
		t.Fatalf("success Expected.Errors = %v, want 0", success.Expected.Errors)
	}
}

func TestFindMissing(t *testing.T) {
	reg, err := Load(writeRegistry(t, validRegistry))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := reg.Find("does-not-exist"); err == nil {
		t.Fatal("Find() expected error for missing case")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("Load() expected error for missing file")
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "missing version",
			content: `cases:
  - id: a
    task_packet: p.yaml
    suite: s
`,
		},
		{
			name: "no cases",
			content: `version: 1
cases: []
`,
		},
		{
			name: "missing id",
			content: `version: 1
cases:
  - task_packet: p.yaml
    suite: s
`,
		},
		{
			name: "missing task_packet",
			content: `version: 1
cases:
  - id: a
    suite: s
`,
		},
		{
			name: "missing suite",
			content: `version: 1
cases:
  - id: a
    task_packet: p.yaml
`,
		},
		{
			name: "duplicate id",
			content: `version: 1
cases:
  - id: a
    task_packet: p.yaml
    suite: s
  - id: a
    task_packet: p2.yaml
    suite: s2
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(writeRegistry(t, tt.content)); err == nil {
				t.Fatalf("Load() expected validation error for %s", tt.name)
			}
		})
	}
}
