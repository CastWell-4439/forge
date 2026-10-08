package harness

import (
	"github.com/castwell/forge/internal/agent/core"
)

// Anchor-and-delta estimation (N3 follow-up).
//
// The provider reports the exact prompt-token count of every request. The
// previous design used that number only to learn a RATIO, and then estimated
// the whole list again next step — which throws away the one exact
// measurement available.
//
// The better shape is to keep the truth as a base and estimate only what was
// added since:
//
//	estimate(step n) = anchor.tokens + estimate(messages[anchor.count:])
//
// The anchor is the previous request's reported count plus how many messages it
// covered. The error is then confined to the NEW messages instead of being
// spread across the entire history — and on a long run the history is most of
// the tokens.
//
// Why an anchor is not simply "the last count": between two requests the
// conversation grows by the assistant's reply and the tool results, and it is
// exactly that growth the next decision is about. Anchoring keeps the part we
// know exact and asks the estimate only about the part we do not.
//
// When the anchor does not apply, the estimate falls back to the full-list
// heuristic. Those cases are enumerated in resetAnchor and are not errors —
// they are the situations where "previous request" and "this request" are not
// the same conversation anymore.

// tokenAnchor records what the provider said about a request that this
// conversation was built from.
type tokenAnchor struct {
	// tokens is the provider's reported prompt-token count for that request.
	tokens int
	// count is how many messages that request covered. The next estimate
	// counts only messages beyond it.
	count int
	// valid reports whether an anchor is in force. A zero value is invalid,
	// which is the right default: a fresh conversation has nothing to anchor to.
	valid bool
}

// anchoredEstimate returns the exact base and the estimated delta.
//
// The delta is NOT calibrated and the base is NOT calibrated: the base is a
// measurement, and the delta is a raw heuristic count that the caller
// calibrates once. Calibrating them separately would apply the ratio to a
// number that is already exact — inflating the anchor by the very error the
// anchor exists to remove.
func (cm *ContextManager) anchoredEstimate(messages []core.Message) (base, delta int) {
	if cm == nil || !cm.anchor.valid {
		return 0, EstimateTokens(messages)
	}
	// A conversation can only shrink when it was compacted, and compaction
	// resets the anchor. A shorter list here means the assumption is broken
	// (a caller replaced the slice), so the safe reading is "start over"
	// rather than "the delta is negative".
	if len(messages) < cm.anchor.count {
		return 0, EstimateTokens(messages)
	}
	return cm.anchor.tokens, EstimateTokens(messages[cm.anchor.count:])
}

// observeAnchor records the provider's count as the base for the next step.
//
// It is called with the message list that was SENT, not the one about to be
// sent: the count describes that list, and the next estimate adds whatever
// comes after it.
func (cm *ContextManager) observeAnchor(sent []core.Message, promptTokens int) {
	if cm == nil || promptTokens <= 0 {
		return
	}
	cm.anchor = tokenAnchor{tokens: promptTokens, count: len(sent), valid: true}
}

// resetAnchor drops the anchor, forcing the next estimate to count the whole
// list.
//
// It is called wherever the conversation stops being an extension of the
// previous request:
//
//   - compaction replaced the list with a summary, so the old boundary and the
//     old count describe content that is no longer there;
//   - a resume rebuilt the list from a checkpoint or journal, which has no
//     relationship to whatever this manager saw last;
//   - the system prompt changed, because the anchor covers a prefix that is no
//     longer the prefix being sent.
//
// The failure mode of forgetting a reset is quiet: the estimate stays roughly
// right for a while and then drifts, because the base is exact but the delta is
// measured from the wrong place.
func (cm *ContextManager) resetAnchor() {
	if cm == nil {
		return
	}
	cm.anchor = tokenAnchor{}
}

// HasAnchor reports whether an anchor is in force. Exposed for tests and for
// diagnostics: "the estimate is anchored to a real measurement" is a different
// claim from "the estimate is a guess", and an operator looking at a surprising
// budget decision should be able to tell which one they are looking at.
func (cm *ContextManager) HasAnchor() bool {
	return cm != nil && cm.anchor.valid
}
