package report

import (
	"fmt"
	"strings"

	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// badCase is the serialized shape of a ForgeX bad case. It captures enough of a
// run to replay and regression-test the failure later.
type badCase struct {
	ID               string        `yaml:"id"`
	Title            string        `yaml:"title"`
	Source           string        `yaml:"source"`
	RunID            string        `yaml:"run_id"`
	FailureCategory  string        `yaml:"failure_category"`
	ExpectedDecision string        `yaml:"expected_decision"`
	Replay           badCaseReplay `yaml:"replay"`
	Assertions       []string      `yaml:"assertions"`
}

// badCaseReplay holds the inputs needed to replay the run.
type badCaseReplay struct {
	TaskPacket model.TaskPacket `yaml:"task_packet"`
}

// GenerateBadCaseYAML distills a run snapshot into a parseable bad-case YAML
// document. The result records the failure category, the expected stop decision
// and a set of human-readable assertions a future eval can check against a replay.
func GenerateBadCaseYAML(snapshot RunSnapshot) ([]byte, error) {
	bc := badCase{
		ID:               badCaseID(snapshot),
		Title:            badCaseTitle(snapshot),
		Source:           badCaseSource(snapshot),
		RunID:            snapshot.Run.ID,
		FailureCategory:  firstCategory(snapshot),
		ExpectedDecision: finalDecisionAction(snapshot),
		Replay:           badCaseReplay{TaskPacket: snapshot.TaskPacket},
		Assertions:       badCaseAssertions(snapshot),
	}

	out, err := yaml.Marshal(bc)
	if err != nil {
		return nil, fmt.Errorf("marshal bad case yaml: %w", err)
	}
	return out, nil
}

// badCaseID derives a stable id from the matched taxonomy rule when available,
// otherwise from the failure category or fingerprint.
func badCaseID(snapshot RunSnapshot) string {
	if len(snapshot.Errors) > 0 {
		e := snapshot.Errors[0]
		if e.Metadata != nil {
			if id := strings.TrimSpace(e.Metadata["rule_id"]); id != "" {
				return id
			}
		}
		if e.Fingerprint != "" {
			return "FORGEX_BADCASE_" + e.Fingerprint
		}
	}
	if cat := firstCategory(snapshot); cat != "" && cat != "unknown" {
		return "FORGEX_BADCASE_" + strings.ToUpper(cat)
	}
	return "FORGEX_BADCASE"
}

// badCaseTitle builds a short descriptive title from the task packet.
func badCaseTitle(snapshot RunSnapshot) string {
	if t := strings.TrimSpace(snapshot.TaskPacket.Name); t != "" {
		return t
	}
	if g := strings.TrimSpace(snapshot.TaskPacket.Goal); g != "" {
		return g
	}
	return "ForgeX bad case"
}

// badCaseSource reports where the case originated, preferring the taxonomy rule
// source, then the task packet metadata, defaulting to real_badcase.
func badCaseSource(snapshot RunSnapshot) string {
	if len(snapshot.Errors) > 0 && snapshot.Errors[0].Metadata != nil {
		if src := strings.TrimSpace(snapshot.Errors[0].Metadata["source"]); src != "" {
			return src
		}
	}
	if src := strings.TrimSpace(snapshot.TaskPacket.Metadata["source"]); src != "" {
		return src
	}
	return "real_badcase"
}

// firstCategory returns the category of the first recorded error, if any.
func firstCategory(snapshot RunSnapshot) string {
	if len(snapshot.Errors) == 0 {
		return ""
	}
	return snapshot.Errors[0].Category
}

// finalDecisionAction returns the action of the last stop decision as a plain
// string suitable for an expected_decision field.
func finalDecisionAction(snapshot RunSnapshot) string {
	if len(snapshot.StopDecisions) == 0 {
		return ""
	}
	return string(snapshot.StopDecisions[len(snapshot.StopDecisions)-1].Action)
}

// badCaseAssertions builds the regression assertions for the case based on the
// observed category and final decision.
func badCaseAssertions(snapshot RunSnapshot) []string {
	var assertions []string
	if cat := firstCategory(snapshot); cat != "" {
		assertions = append(assertions, fmt.Sprintf("error.category == %s", cat))
	}
	if action := finalDecisionAction(snapshot); action != "" {
		assertions = append(assertions, fmt.Sprintf("stop_decision.action == %s", action))
	}
	return assertions
}
