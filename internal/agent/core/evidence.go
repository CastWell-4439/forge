package core

import "time"

// Evidence aggregation: metadata and the four relations (memory governance).
//
// Long-term memory used to be a flat record — id, content, category, createdAt —
// and recall returned the topK by similarity, each independent of the others.
// That is enough to find things and not enough to trust them:
//
//   - a memory carried no provenance, so "the agent wrote this" and "a human
//     confirmed this" looked identical;
//   - nothing knew that two recalled memories were ABOUT THE SAME THING, which
//     is the only situation where duplicate, conflicting or stale evidence
//     matters;
//   - conflicting memories were returned side by side with no signal that they
//     disagreed, leaving the model to pick one silently.
//
// The four relations below are the vocabulary that fixes the third point. They
// are not "quality levels": they describe how two pieces of evidence relate,
// and each has a different correct handling.
//
//   duplicate — same claim, different sources     -> vote together, raise confidence
//   complementary — one aspect each, no conflict  -> merge, keep both
//   conflicting — mutually exclusive claims       -> do NOT silently pick one
//   stale — same claim, older observation         -> newest wins, history noted
//
// The handling of "conflicting" is the one worth stating out loud: when two
// claims disagree and neither is clearly better, the honest answer is to
// present both and SAY they conflict. Silently choosing produces a confident
// wrong answer, and the model has no way to notice.

// EvidenceRelation describes how one piece of evidence relates to others about
// the same subject.
type EvidenceRelation string

const (
	// RelationDuplicate: the same claim from different sources. These vote
	// together — agreement between independent observations is evidence.
	RelationDuplicate EvidenceRelation = "duplicate"
	// RelationComplementary: different aspects, no disagreement. Both are kept.
	RelationComplementary EvidenceRelation = "complementary"
	// RelationConflicting: mutually exclusive claims about one subject.
	RelationConflicting EvidenceRelation = "conflicting"
	// RelationStale: an older observation of a claim that a newer one also
	// makes. The newer one wins; the older is noted rather than discarded.
	RelationStale EvidenceRelation = "stale"
)

// MemorySource identifies where a memory came from.
//
// It is a free-form string with conventional prefixes rather than an enum: the
// set of producers grows (a run, a distilled lesson, a human review, a future
// importer), and an enum would force a code change for each one. The prefix
// makes the origin readable in a prompt without a lookup.
//
//	run:<session-id>      the agent distilled it from its own run
//	lesson:<lesson-id>    the control plane distilled it from a finished run
//	human:<who>           a person confirmed or wrote it
type MemorySource string

// Source kind prefixes. Kept as constants so producers and readers agree on the
// spelling; a typo here would make a human-confirmed memory look agent-written,
// which is exactly the distinction the field exists for.
const (
	MemorySourceRun    = "run:"
	MemorySourceLesson = "lesson:"
	MemorySourceHuman  = "human:"
)

// Kind reports the source's kind without its identifier, or "" when the source
// is unset. Used for the trust weighting: a human-confirmed claim outranks a
// self-distilled one.
func (s MemorySource) Kind() string {
	for _, prefix := range []string{MemorySourceRun, MemorySourceLesson, MemorySourceHuman} {
		if len(s) >= len(prefix) && string(s[:len(prefix)]) == prefix {
			return prefix[:len(prefix)-1]
		}
	}
	return ""
}

// IsHumanConfirmed reports whether a person vouched for this memory.
func (s MemorySource) IsHumanConfirmed() bool { return s.Kind() == "human" }

// DefaultConfidence is what an entry with no stated confidence is worth.
//
// It is the middle of the scale rather than the top or the bottom: an entry
// written before this field existed is neither vouched for nor suspect, and
// treating it as either would silently re-rank every existing memory.
const DefaultConfidence = 0.5

// ConfidenceOf resolves an entry's confidence, applying the default.
func ConfidenceOf(entry MemoryEntry) float64 {
	if entry.Confidence <= 0 {
		return DefaultConfidence
	}
	if entry.Confidence > 1 {
		return 1
	}
	return entry.Confidence
}

// ObservedAtOf is when the fact was true, falling back to when it was written.
//
// The distinction matters for the stale relation: "we learned this last week
// about last year" and "we learned this last week about last week" age
// differently. Not every producer knows the observation time, so the write time
// is the honest fallback — it is never later than the observation, which errs
// toward treating a claim as newer than it is. That direction is the safe one
// for memory: an over-fresh claim is still visible, while an over-stale one
// gets pushed out of the way.
func ObservedAtOf(entry MemoryEntry) time.Time {
	if !entry.ObservedAt.IsZero() {
		return entry.ObservedAt
	}
	return entry.CreatedAt
}

// EvidenceGroup is a set of memories that are about the same subject.
//
// Grouping is what makes the other relations computable: duplicate, conflicting
// and stale are all questions about entries IN a group. Across groups the
// relation is complementary by construction — two memories about different
// subjects cannot disagree.
type EvidenceGroup struct {
	// Subject is the group's representative claim, used for the prompt line and
	// as the label a conflict report refers to.
	Subject string
	// Members are the entries in this group, ordered by the aggregation's final
	// ranking.
	Members []MemoryEntry
	// Relation is how the members relate. A group with a single member is
	// trivially complementary.
	Relation EvidenceRelation
	// Confidence is the aggregated confidence: independent agreement raises it,
	// a conflict caps it.
	Confidence float64
	// Sources lists every distinct source behind the group's claim, so the
	// prompt can show that three independent runs agreed rather than one run
	// being repeated.
	Sources []MemorySource
}

// MemoryEvidence is the result of aggregating a recall.
type MemoryEvidence struct {
	// Groups are the aggregated memories, best first.
	Groups []EvidenceGroup
	// Conflicts lists the subjects where the evidence disagreed and no claim
	// was clearly better. They are reported rather than resolved — see the
	// package comment on why silently choosing is the wrong move.
	Conflicts []EvidenceGroup
}
