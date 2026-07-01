package lessons

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/report"
)

// haltingActions are the terminal stop actions that make a run worth learning
// from. A run whose final decision is continue or retry is not a bad case.
var haltingActions = map[model.StopAction]bool{
	model.StopActionStop:     true,
	model.StopActionEscalate: true,
	model.StopActionPause:    true,
}

// Derive extracts durable Lesson records from a completed run snapshot.
//
// A lesson is produced only when the run ended in a halting outcome (its final
// stop decision is stop/escalate/pause) and at least one classified error was
// recorded. One lesson is emitted per recorded error. A clean run that
// continued or succeeded yields nil, so the happy-path demo never produces a
// misleading lesson.
//
// Derive performs no I/O and is deterministic: lesson timestamps are taken from
// the run's end (or start) time rather than the wall clock.
func Derive(snapshot report.RunSnapshot) []model.Lesson {
	if !haltingActions[finalAction(snapshot)] {
		return nil
	}
	if len(snapshot.Errors) == 0 {
		return nil
	}

	createdAt := snapshot.Run.EndedAt
	if createdAt.IsZero() {
		createdAt = snapshot.Run.StartedAt
	}

	action := string(finalAction(snapshot))
	failedValidationIDs := failedContractValidationIDs(snapshot)

	derived := make([]model.Lesson, 0, len(snapshot.Errors))
	for _, e := range snapshot.Errors {
		derived = append(derived, lessonFromError(snapshot.Run.ID, e, action, failedValidationIDs, createdAt))
	}
	return derived
}

// lessonFromError builds a single Lesson from a classified error envelope.
func lessonFromError(runID string, e model.ErrorEnvelope, finalDecision string, failedValidationIDs []string, createdAt time.Time) model.Lesson {
	rule := meta(e, "rule_id")
	recommendation := meta(e, "recommendation")

	content := recommendation
	if content == "" {
		content = e.Message
	}

	metadata := map[string]string{
		"final_decision": finalDecision,
	}
	if rule != "" {
		metadata["rule"] = rule
	}
	if e.Category != "" {
		metadata["category"] = e.Category
	}
	if e.Severity != "" {
		metadata["severity"] = e.Severity
	}
	if e.Fingerprint != "" {
		metadata["fingerprint"] = e.Fingerprint
	}
	if trigger := triggerText(e); trigger != "" {
		metadata["trigger"] = trigger
	}
	if evidence := evidenceIDs(e, failedValidationIDs); len(evidence) > 0 {
		metadata["evidence"] = strings.Join(evidence, ",")
	}

	return model.Lesson{
		ID:          lessonID(e),
		Title:       lessonTitle(e),
		SourceRunID: runID,
		Category:    orString(e.Category, "unknown"),
		Content:     content,
		Metadata:    metadata,
		CreatedAt:   createdAt,
	}
}

// lessonID derives a stable id, preferring the matched taxonomy rule, then the
// fingerprint, then the category.
func lessonID(e model.ErrorEnvelope) string {
	if rule := meta(e, "rule_id"); rule != "" {
		return "LESSON_" + rule
	}
	if e.Fingerprint != "" {
		return "LESSON_" + e.Fingerprint
	}
	if e.Category != "" {
		return "LESSON_" + strings.ToUpper(e.Category)
	}
	return "LESSON_UNKNOWN"
}

// lessonTitle builds a short human-readable title from the error operation.
func lessonTitle(e model.ErrorEnvelope) string {
	op := strings.TrimSpace(e.Operation)
	if op == "" {
		op = strings.TrimSpace(e.Source)
	}
	if op == "" {
		return "Prevent recurrence of classified failure"
	}
	return fmt.Sprintf("Prevent %s failure in %s", orString(e.Category, "unknown"), op)
}

// triggerText describes what tripped the failure, e.g.
// "required_assets is empty for demo.expensive_generation".
func triggerText(e model.ErrorEnvelope) string {
	msg := strings.TrimSpace(e.Message)
	op := strings.TrimSpace(e.Operation)
	switch {
	case msg != "" && op != "":
		return fmt.Sprintf("%s for %s", msg, op)
	case msg != "":
		return msg
	default:
		return op
	}
}

// evidenceIDs collects the ids that back the lesson: the error id and any
// failed contract validation ids.
func evidenceIDs(e model.ErrorEnvelope, failedValidationIDs []string) []string {
	ids := make([]string, 0, len(failedValidationIDs)+1)
	if e.ID != "" {
		ids = append(ids, e.ID)
	}
	ids = append(ids, failedValidationIDs...)
	return ids
}

// failedContractValidationIDs returns the ids of every failed contract
// validation in the snapshot, sorted for deterministic output.
func failedContractValidationIDs(snapshot report.RunSnapshot) []string {
	var ids []string
	for _, v := range snapshot.ContractValidations {
		if v.Status == "failed" && v.ID != "" {
			ids = append(ids, v.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// finalAction returns the action of the last recorded stop decision.
func finalAction(snapshot report.RunSnapshot) model.StopAction {
	if len(snapshot.StopDecisions) == 0 {
		return ""
	}
	return snapshot.StopDecisions[len(snapshot.StopDecisions)-1].Action
}

func meta(e model.ErrorEnvelope, key string) string {
	if e.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(e.Metadata[key])
}

func orString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
