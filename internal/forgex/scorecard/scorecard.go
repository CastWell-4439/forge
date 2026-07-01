package scorecard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/metrics"
	"github.com/castwell/forge/internal/forgex/model"
)

const (
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictUnknown = "unknown"
)

// Scorecard is the deterministic quality summary for a ForgeX run.
type Scorecard struct {
	RunID      string            `json:"run_id" yaml:"run_id"`
	CaseID     string            `json:"case_id,omitempty" yaml:"case_id,omitempty"`
	SuiteID    string            `json:"suite_id" yaml:"suite_id"`
	Overall    string            `json:"overall" yaml:"overall"`
	Dimensions map[string]string `json:"dimensions" yaml:"dimensions"`
	Metrics    Metrics           `json:"metrics" yaml:"metrics"`
	CreatedAt  time.Time         `json:"created_at" yaml:"created_at"`
}

// Metrics records the small set of counts used by the initial scorecard.
type Metrics struct {
	Errors           int `json:"errors" yaml:"errors"`
	PolicyDenies     int `json:"policy_denies" yaml:"policy_denies"`
	ValidationFailed int `json:"validation_failed" yaml:"validation_failed"`
	ArtifactsMissing int `json:"artifacts_missing" yaml:"artifacts_missing"`
	StateConflicts   int `json:"state_conflicts" yaml:"state_conflicts"`
	Lessons          int `json:"lessons" yaml:"lessons"`
	EvalFailedCases  int `json:"eval_failed_cases" yaml:"eval_failed_cases"`
}

// Build derives a scorecard from persisted run artifacts and an eval result.
func Build(artifacts forgexeval.RunArtifacts, evalResult model.EvalResult, lessonCount int) Scorecard {
	control := metrics.Compute(metrics.Inputs{
		PolicyDecisions:     artifacts.PolicyDecisions,
		ContractValidations: artifacts.ContractValidations,
		StopDecisions:       artifacts.StopDecisions,
		StopSignals:         artifacts.StopSignals,
		Artifacts:           artifacts.Artifacts,
		StateClaims:         artifacts.StateClaims,
		WorldState:          &artifacts.WorldState,
	})

	m := Metrics{
		Errors:           len(artifacts.Errors),
		PolicyDenies:     control.PolicyDenyCount,
		ValidationFailed: control.ContractValidationFailedCount,
		ArtifactsMissing: control.MissingArtifactCount,
		StateConflicts:   control.StateConflictCount,
		Lessons:          lessonCount,
		EvalFailedCases:  failedEvalCases(evalResult),
	}

	dimensions := map[string]string{
		"task_success":        taskSuccessVerdict(artifacts, evalResult),
		"tool_usage_quality":  toolUsageVerdict(m),
		"control_quality":     controlQualityVerdict(m, evalResult),
		"operational_quality": VerdictUnknown,
	}

	overall := VerdictPass
	for _, verdict := range dimensions {
		if verdict == VerdictFail {
			overall = VerdictFail
			break
		}
	}
	if evalResult.Status == model.EvalFailed {
		overall = VerdictFail
	}

	return Scorecard{
		RunID:      artifacts.Run.ID,
		CaseID:     firstEvalCaseID(evalResult),
		SuiteID:    evalResult.SuiteID,
		Overall:    overall,
		Dimensions: dimensions,
		Metrics:    m,
		CreatedAt:  time.Now().UTC(),
	}
}

// Write writes scorecard.json into the run directory.
func Write(runDir string, card Scorecard) error {
	encoded, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(filepath.Join(runDir, "scorecard.json"), encoded, 0o644)
}

// AppendToReport appends a stable Scorecard section to report.md. It is
// idempotent: if the section already exists, the report is left unchanged.
func AppendToReport(runDir string, card Scorecard) error {
	path := filepath.Join(runDir, "report.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.Contains(string(data), "## Scorecard\n") {
		return nil
	}
	updated := string(data)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "\n" + Markdown(card)
	return os.WriteFile(path, []byte(updated), 0o644)
}

// Markdown renders a human-readable Scorecard section.
func Markdown(card Scorecard) string {
	var b strings.Builder
	b.WriteString("## Scorecard\n\n")
	b.WriteString(fmt.Sprintf("- **Overall**: %s\n", card.Overall))
	b.WriteString(fmt.Sprintf("- **Task Success**: %s\n", card.Dimensions["task_success"]))
	b.WriteString(fmt.Sprintf("- **Tool Usage Quality**: %s\n", card.Dimensions["tool_usage_quality"]))
	b.WriteString(fmt.Sprintf("- **Control Quality**: %s\n", card.Dimensions["control_quality"]))
	b.WriteString(fmt.Sprintf("- **Operational Quality**: %s\n", card.Dimensions["operational_quality"]))
	b.WriteString(fmt.Sprintf("- **Errors**: %d\n", card.Metrics.Errors))
	b.WriteString(fmt.Sprintf("- **Policy Denies**: %d\n", card.Metrics.PolicyDenies))
	b.WriteString(fmt.Sprintf("- **Validation Failed**: %d\n", card.Metrics.ValidationFailed))
	b.WriteString(fmt.Sprintf("- **Artifacts Missing**: %d\n", card.Metrics.ArtifactsMissing))
	b.WriteString(fmt.Sprintf("- **State Conflicts**: %d\n", card.Metrics.StateConflicts))
	b.WriteString(fmt.Sprintf("- **Lessons**: %d\n", card.Metrics.Lessons))
	b.WriteString("\n")
	return b.String()
}

func failedEvalCases(result model.EvalResult) int {
	failed := 0
	for _, c := range result.Cases {
		if c.Status == model.EvalFailed {
			failed++
		}
	}
	return failed
}

func firstEvalCaseID(result model.EvalResult) string {
	if len(result.Cases) == 0 {
		return ""
	}
	return result.Cases[0].ID
}

func taskSuccessVerdict(artifacts forgexeval.RunArtifacts, result model.EvalResult) string {
	if result.Status == model.EvalFailed {
		return VerdictFail
	}
	switch artifacts.Run.Status {
	case model.RunSucceeded:
		return VerdictPass
	case model.RunStopped, model.RunPaused, model.RunEscalated, model.RunFailed:
		if result.Status == model.EvalPassed {
			return VerdictPass
		}
		return VerdictFail
	default:
		return VerdictUnknown
	}
}

func toolUsageVerdict(m Metrics) string {
	if m.PolicyDenies > 0 || m.ValidationFailed > 0 || m.ArtifactsMissing > 0 {
		return VerdictFail
	}
	return VerdictPass
}

func controlQualityVerdict(m Metrics, result model.EvalResult) string {
	if result.Status == model.EvalFailed || m.StateConflicts > 0 {
		return VerdictFail
	}
	return VerdictPass
}

// Format renders a stable human-readable single-line summary.
func Format(card Scorecard) string {
	return fmt.Sprintf("run_id=%s suite=%s overall=%s task=%s tool=%s control=%s operational=%s",
		card.RunID,
		card.SuiteID,
		card.Overall,
		card.Dimensions["task_success"],
		card.Dimensions["tool_usage_quality"],
		card.Dimensions["control_quality"],
		card.Dimensions["operational_quality"],
	)
}
