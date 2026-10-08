package harness

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// The citation contract (memory governance, part three).
//
// Parts one and two built the machinery: aggregation for recall, and a
// lifecycle that archives what has been offered many times and used never. The
// second part shipped with a hole, recorded honestly at the time — the agent
// could not tell whether a recalled memory was used, so every observation
// carried UsageKnown=false and no archive decision could ever be justified.
//
// This file closes the hole the cheap way: the model is INVITED to list the ids
// it relied on. That is all. Not required, not inferred, not inferred-from-text.
//
// Three rules make the invitation safe to act on:
//
//  1. SILENCE IS NOT DISUSE. A response with no `used_memory` yields no usage
//     signal at all. Reading it as "declined" would archive every memory any
//     agent ever saw, which is the failure the whole lifecycle was designed
//     around.
//  2. A CITATION MUST BE VERIFIABLE. Only ids actually recalled to this run
//     count. A model can invent an id, and an invented citation is a fabricated
//     fact — the exact thing this design exists to avoid. Unknown ids are
//     dropped and reported.
//  3. A CITATION IS NOT A SCORE. Citing a memory says "I used this", nothing
//     about how much. The lifecycle only ever needs the binary, and asking for
//     more would push the model to make judgements it cannot support.

// CitationResult is what a response's citations resolved to.
type CitationResult struct {
	// Known are ids that were both cited AND recalled to this run.
	Known []string
	// Unknown are cited ids that were never recalled to this run: either a
	// hallucinated id or a misremembered one.
	Unknown []string
	// Reported is whether the model said anything at all.
	//
	// It is the field the lifecycle reads. Reported=false means the run had an
	// opinion-free relationship with what it was shown; Reported=true with an
	// empty Known list means the model listed ids and none of them checked out,
	// which is also not evidence of disuse.
	Reported bool
}

// UsageKnown reports whether this result carries a usable usage signal.
//
// It is true only when the model made a statement we could verify against what
// it was shown. "It cited something real" is the only case that tells us
// anything.
func (c CitationResult) UsageKnown() bool { return len(c.Known) > 0 }

// resolveCitations checks a response's cited ids against what was recalled.
//
// recalled is the set of ids this run actually offered. Anything outside it is
// dropped rather than trusted: a citation for a memory the run never saw cannot
// be evidence about that memory, and accepting it would let a hallucinating
// model steer the archive.
func resolveCitations(cited []string, recalled map[string]bool) CitationResult {
	result := CitationResult{Reported: len(cited) > 0}
	if len(cited) == 0 {
		return result
	}

	seen := map[string]bool{}
	for _, raw := range cited {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if recalled[id] {
			result.Known = append(result.Known, id)
			continue
		}
		result.Unknown = append(result.Unknown, id)
	}
	return result
}

// recalledIDs collects the ids a recall offered, which is the set a citation is
// checked against.
func recalledIDs(evidence core.MemoryEvidence) map[string]bool {
	out := map[string]bool{}
	for _, g := range evidence.Groups {
		for _, m := range g.Members {
			if m.ID != "" {
				out[m.ID] = true
			}
		}
	}
	return out
}

// usageObservations turns a run's verified citations into observations.
//
// The asymmetry is the point, and it is worth stating plainly:
//
//   - a VERIFIED citation produces an observation with UsageKnown=true and
//     Used=true, which is evidence that the memory earns its place;
//   - anything else produces NOTHING.
//
// Note that there is no path producing "UsageKnown=true, Used=false". That
// combination would say "this was offered and declined", and this contract
// cannot establish it: the model not mentioning a memory is not a statement
// about it. The lifecycle's archive decision needs the declined case; until a
// contract can produce it honestly, that decision stays unreachable in
// practice — which is the honest state of the feature, not a bug to paper over
// by inventing the signal.
func usageObservations(runID string, citations CitationResult, at time.Time) []core.MemoryObservation {
	if runID == "" || len(citations.Known) == 0 {
		return nil
	}
	out := make([]core.MemoryObservation, 0, len(citations.Known))
	for _, id := range citations.Known {
		out = append(out, core.MemoryObservation{
			RunID:      runID,
			EntryID:    id,
			Kind:       core.KindMemory,
			Used:       true,
			UsageKnown: true,
			At:         at,
		})
	}
	return out
}

// recordCitations resolves this run's citations and hands the verified ones to
// the sink.
//
// Nothing is recorded when the model said nothing, when it cited only ids it
// was never shown, or when no sink is configured. All three are the same
// outcome for the same reason: the run produced no usable usage signal, and
// recording a weaker one would be a fabrication that the archive decision
// would then act on.
func (l *AgentLoop) recordCitations(ctx context.Context, sessionID string) {
	if l == nil || l.usageSink == nil || len(l.citedThis) == 0 {
		return
	}
	resolved := resolveCitations(l.citedThis, l.recalledThis)

	if len(resolved.Unknown) > 0 {
		// Reported, not acted on: a cited id that was never recalled is either a
		// hallucination or a confusion between runs, and either way it says
		// nothing about the memory it names.
		log.Printf("[harness] run %s cited %d memory id(s) it was never shown (%v); ignoring them",
			sessionID, len(resolved.Unknown), resolved.Unknown)
	}
	if !resolved.UsageKnown() {
		// The model cited nothing verifiable. This is the common case and it is
		// NOT a negative signal — see citation.go rule 1.
		return
	}

	if err := l.usageSink.RecordUsage(ctx, sessionID, resolved.Known); err != nil {
		// Never fatal: bookkeeping for a later offline decision must not fail a
		// run that already produced its answer.
		log.Printf("[INFO] could not record memory usage for run %s: %v", sessionID, err)
	}
}
