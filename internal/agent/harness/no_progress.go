package harness

import (
	"encoding/json"
	"fmt"

	"github.com/castwell/forge/internal/agent/core"
)

// No-progress detection: the loop's answer to "the model is stuck".
//
// Two mechanisms, one fingerprint:
//
//	duplicate guard — a NON-IDEMPOTENT call whose identical twin already ran in
//	                  this run is refused before the handler executes. This is
//	                  the runtime form of the project's rule "when in doubt,
//	                  stop rather than repeat a side effect": the ledger always
//	                  claimed replaying such a tool would double its effect, and
//	                  before this it was only a claim.
//	streak stop    — the same fingerprint for NoProgressThreshold consecutive
//	                  tool calls ends the run with Reason "no_progress", the
//	                  same honest-stop family as "max_steps": work so far is
//	                  kept and explained, not discarded as an error.
//
// The fingerprint is the tool name plus its canonical parameters: encoding/json
// marshals map keys in sorted order, so parameter order cannot mask a repeat.
// Changing the parameters or the tool changes the fingerprint, which resets the
// streak — the model gets out of the loop the moment it genuinely changes tack.

// DefaultNoProgressThreshold is how many consecutive identical tool calls
// count as no progress. Three repeats is past the point of a coincidence; 0
// disables the check entirely.
const DefaultNoProgressThreshold = 3

// toolFingerprint identifies a call by name + canonical parameters.
func toolFingerprint(tool string, params map[string]any) string {
	encoded, err := json.Marshal(params)
	if err != nil {
		// Unencodable params are still a specific, stable string — falling
		// back to fmt keeps the fingerprint defined rather than panicking.
		encoded = []byte(fmt.Sprintf("%v", params))
	}
	return tool + "\x00" + string(encoded)
}

// compactParams renders parameters for a refusal message: short, stable, and
// showing the model exactly which call was refused.
func compactParams(params map[string]any) string {
	if len(params) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Sprintf("%v", params)
	}
	const maxLen = 120
	if len(encoded) > maxLen {
		return string(encoded[:maxLen]) + "..."
	}
	return string(encoded)
}

// initProgressTracking rebuilds duplicate-detection state from the ledger.
//
// Called with the ledger both a fresh run starts with and a resume restores
// from its checkpoint, so a resumed run keeps refusing calls that ran before
// the crash — the guard is about side effects, and side effects survive the
// process. The consecutive streak intentionally starts at zero: it measures
// the current loop's behaviour, and a resume is a fresh attempt.
func (l *AgentLoop) initProgressTracking(ledger []core.ToolCallRecord) {
	l.nonIdempotentDone = make(map[string]bool, len(ledger))
	l.lastFingerprint = ""
	l.noProgress = 0

	for _, record := range ledger {
		if record.Status != core.ToolCallCompleted || record.Idempotent {
			continue
		}
		l.nonIdempotentDone[toolFingerprint(record.Tool, record.Params)] = true
	}
}

// noteProgress records one observed call and returns the new consecutive
// count for that fingerprint.
func (l *AgentLoop) noteProgress(fingerprint string) int {
	if fingerprint == l.lastFingerprint {
		l.noProgress++
	} else {
		l.lastFingerprint = fingerprint
		l.noProgress = 1
	}
	return l.noProgress
}

// noProgressThreshold resolves the configured threshold: 0 (unset) means the
// default, a negative value explicitly disables the streak stop. The duplicate
// guard for non-idempotent tools is NOT configurable — replaying a side effect
// is never correct, so there is no setting that should allow it.
func (l *AgentLoop) noProgressThreshold() int {
	if l.config.NoProgressThreshold < 0 {
		return 0
	}
	if l.config.NoProgressThreshold == 0 {
		return DefaultNoProgressThreshold
	}
	return l.config.NoProgressThreshold
}
