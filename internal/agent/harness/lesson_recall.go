package harness

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// Lessons recall (F3): the agent starts a run already knowing what the control
// plane learned from earlier runs.
//
// Three properties are deliberate and tested:
//
//  1. Read-only. Lessons flow one way (control plane -> agent). The agent does
//     not write lessons: its own verdicts come from ForgeX's snapshot-based
//     derivation, which has run evidence the agent does not. Letting the agent
//     write would create lessons with no run to justify them.
//  2. Gated. Every recalled lesson passes defaultLessonFilter (or a caller's
//     replacement) before it can reach the prompt. The default gate includes
//     the loop-protection rule: a lesson produced BY this run is never fed
//     back into it — that is how a system starts teaching itself its own
//     mistakes.
//  3. Never fatal. A broken lessons store is an enhancement outage, not a run
//     failure: recall errors are logged and the run proceeds exactly as before.
const defaultLessonTopK = 3

// defaultLessonFilter is the deterministic read-side gate. It is the
// counterpart of defaultMemoryWriteJudge: no model call, same decision every
// time, and conservative in the direction that matters (keeping noise out of
// the prompt costs a better lesson; admitting noise costs correctness).
//
// Rules:
//   - a lesson produced by THIS session is dropped (self-feedback guard);
//   - a lesson with no usable text is dropped (nothing to say);
//   - otherwise it is admitted.
//
// Relevance is not re-checked here: the source already ranked by the query.
// A filter that second-guessed ranking would need the same index twice.
func defaultLessonFilter(_ context.Context, sessionID string, item core.RecallItem) bool {
	if strings.TrimSpace(item.Content) == "" && strings.TrimSpace(item.Title) == "" {
		return false
	}
	// Self-feedback guard: SourceRunID equal to the running session means this
	// lesson was derived from this very run's own observer record.
	if item.SourceRunID != "" && sessionID != "" && item.SourceRunID == sessionID {
		return false
	}
	return true
}

// recallLessonsInto inserts matching lessons as a system block, the same shape
// recallInto uses for memory (directly after the system prompt) so the model
// sees both channels the same way.
//
// Every failure path returns with the message list untouched.
func (l *AgentLoop) recallLessonsInto(ctx context.Context, messages *[]core.Message, sessionID, userInput string) {
	if l.lessons == nil {
		return
	}
	items, err := l.lessons.Recall(ctx, userInput, defaultLessonTopK)
	if err != nil {
		// An enhancement outage must not fail the run (F3-8).
		log.Printf("[harness] lesson recall failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	filter := l.lessonFilter
	if filter == nil {
		filter = defaultLessonFilter
	}

	var block strings.Builder
	admitted := 0
	for _, item := range items {
		if !filter(ctx, sessionID, item) {
			continue
		}
		if admitted == 0 {
			block.WriteString("Lessons from earlier runs (from the control plane; apply only where they fit this task " +
				"and verify against the current state before relying on them):\n")
		}
		admitted++
		line := fmt.Sprintf("- [%s] ", item.Category)
		if item.Title != "" {
			line += item.Title + ": "
		}
		line += truncate(item.Content, 300)
		if item.SourceRunID != "" {
			// Provenance, so the model can weigh the claim and an operator can
			// trace it. A lesson without a source is an assertion.
			line += fmt.Sprintf(" (from run %s)", item.SourceRunID)
		}
		block.WriteString(line + "\n")
	}
	if admitted == 0 {
		// Everything recalled was filtered out: say nothing rather than
		// leaving an empty header in the prompt.
		return
	}

	msgs := *messages
	if len(msgs) == 0 {
		return
	}
	rest := make([]core.Message, 0, len(msgs))
	rest = append(rest, msgs[1:]...)
	*messages = append([]core.Message{msgs[0], {Role: "system", Content: block.String()}}, rest...)
}
