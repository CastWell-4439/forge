package harness

import (
	"context"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// Memory review: turning the store's contents into proposals.
//
// This is the first half of the reactive review the lifecycle was missing. It
// reads memories and produces CANDIDATES — distill this episodic entry, discard
// that one, verify this claim — and it executes none of them.
//
// The restraint is the design, not a limitation of scope. Every candidate kind
// would change shared state: distilling writes a skill, discarding removes a
// memory, promoting reclassifies one. Memory is shared and its contents are
// model-generated (see core/lifecycle.go), so the review's job is to make the
// decision cheap for a human, not to make it.
//
// What the review can do that the archive could not: it does not need a usage
// signal. "This episodic memory says something a rule can check" and "this entry
// is old and was never promoted" are both decidable from the entry itself,
// which is why this path works where the archive's did not.

// ReviewConfig tunes what the review proposes.
type ReviewConfig struct {
	// EpisodicTTL is how long an episodic memory is worth carrying before the
	// review proposes acting on it.
	EpisodicTTL time.Duration
	// DistillMinRuns is how many distinct runs must describe the same thing
	// before distillation is worth proposing. One run is an anecdote; the same
	// pattern from several runs is a candidate for a reusable skill.
	DistillMinRuns int
	// MaxCandidates caps the proposal list. Unlike a prompt budget, capping a
	// review is safe: a human can re-run it, and a shorter list is easier to
	// act on.
	MaxCandidates int
	// Now is the reference time, injected for deterministic tests.
	Now time.Time
}

// DefaultReviewConfig returns the standard thresholds.
func DefaultReviewConfig() ReviewConfig {
	return ReviewConfig{
		EpisodicTTL:    core.DefaultEpisodicTTL,
		DistillMinRuns: 2,
		MaxCandidates:  50,
	}
}

func (c ReviewConfig) normalize() ReviewConfig {
	d := DefaultReviewConfig()
	if c.EpisodicTTL <= 0 {
		c.EpisodicTTL = d.EpisodicTTL
	}
	if c.DistillMinRuns <= 0 {
		c.DistillMinRuns = d.DistillMinRuns
	}
	if c.MaxCandidates <= 0 {
		c.MaxCandidates = d.MaxCandidates
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	return c
}

// ReviewInput is everything the review reads.
type ReviewInput struct {
	// Memories are the long-term entries under review.
	Memories []core.MemoryEntry
	// Usage is the per-entry observation aggregate, when one exists. It is
	// optional: the review works without it, which is what makes it usable
	// while the archive cannot be.
	Usage map[string]core.UsageStat
}

// MemoryReview is the review's output: proposals, and nothing executed.
type MemoryReview struct {
	Candidates []core.MemoryCandidate
	// FactsChecked is how many fact-layer entries were examined.
	FactsChecked int
	// EpisodicChecked is how many episodic entries were examined.
	EpisodicChecked int
	// AssertionsFound is every claim the extraction passes found, so a reviewer
	// can see what the checker could read at all — an empty assertion list is a
	// finding too (the memory states nothing checkable).
	AssertionsFound []core.Assertion
}

// ReviewMemories produces proposals for a set of memories.
//
// The rules, in the order they are applied:
//
//	episodic, older than TTL, states a reusable pattern   -> propose distill
//	episodic, older than TTL, states nothing checkable    -> propose discard
//	episodic, older than TTL, states a world claim        -> propose promote
//	fact, states an extractable claim                     -> propose verify
//
// The middle rule is where "nothing checkable" becomes actionable rather than
// merely unverifiable: an old episodic entry that makes no claim and describes
// no reusable pattern has no destination, and saying so is more useful than
// keeping it forever.
func ReviewMemories(ctx context.Context, in ReviewInput, extractor core.AssertionExtractor, cfg ReviewConfig) MemoryReview {
	cfg = cfg.normalize()
	var review MemoryReview

	// Patterns seen across episodic entries, counted by their normalised text
	// skeleton. Two runs describing the same thing is the signal distillation
	// wants; one run is an anecdote.
	skeletons := map[string][]core.MemoryEntry{}

	for _, m := range in.Memories {
		layer := core.NormalizeMemoryLayer(string(m.Layer))

		// Collect assertions: the static pass always runs, the model pass adds
		// to it. A model failure degrades to static rather than failing the
		// review — the static pass is the floor, not a fallback of last resort.
		assertions := core.ExtractAssertionsStatic(m.Content)
		if extractor != nil {
			if found, err := extractor.ExtractAssertions(ctx, m.Content); err == nil {
				assertions = core.MergeAssertions(assertions, found)
			}
		}
		review.AssertionsFound = append(review.AssertionsFound, assertions...)

		if layer == core.LayerFact {
			review.FactsChecked++
			if len(assertions) > 0 {
				// Facts are checked against recorded evidence, which the
				// control plane holds (tool calls, errors). The proposal names
				// what to check; the checking itself is the next round's work.
				review.Candidates = append(review.Candidates, core.MemoryCandidate{
					EntryID: m.ID,
					Kind:    core.CandidateVerify,
					Content: m.Content,
					Reason: "fact states " + describeAssertions(assertions) +
						"; check it against recorded run evidence",
					Confidence: 0.5,
					Evidence:   "assertions: " + assertionValues(assertions),
				})
			}
			continue
		}

		review.EpisodicChecked++

		age := cfg.Now.Sub(core.ObservedAtOf(m))
		if age < cfg.EpisodicTTL {
			// Still fresh: an episodic memory's value is highest right after
			// the run that produced it.
			continue
		}

		// A reusable pattern is what makes distillation worth proposing. The
		// grouping below is exact-text for now: it catches the common case (the
		// same summary written twice, because the same failure recurred) without
		// the semantic machinery that conflates different lessons.
		skeletons[patternKey(m.Content)] = append(skeletons[patternKey(m.Content)], m)

		switch {
		case hasReusablePattern(m.Content):
			review.Candidates = append(review.Candidates, core.MemoryCandidate{
				EntryID:    m.ID,
				Kind:       core.CandidateDistill,
				Content:    m.Content,
				Reason:     "episodic memory past its lifetime that describes a repeatable procedure",
				Confidence: 0.6,
				Evidence:   "run: " + m.SourceRunIDOrEmpty(),
			})
		case len(assertions) > 0:
			review.Candidates = append(review.Candidates, core.MemoryCandidate{
				EntryID:    m.ID,
				Kind:       core.CandidatePromote,
				Content:    m.Content,
				Reason:     "episodic memory stating " + describeAssertions(assertions) + ", which may be a durable fact",
				Confidence: 0.4,
				Evidence:   "assertions: " + assertionValues(assertions),
			})
		default:
			review.Candidates = append(review.Candidates, core.MemoryCandidate{
				EntryID:    m.ID,
				Kind:       core.CandidateDiscard,
				Content:    m.Content,
				Reason:     "episodic memory past its lifetime with nothing checkable and no repeatable procedure",
				Confidence: 0.3,
				Evidence:   "run: " + m.SourceRunIDOrEmpty(),
			})
		}
	}

	// Agreement raises a distillation proposal: the same pattern from several
	// runs is exactly the reusable knowledge a skill should encode.
	for _, group := range skeletons {
		if len(group) < cfg.DistillMinRuns {
			continue
		}
		for i := range review.Candidates {
			if review.Candidates[i].Kind != core.CandidateDistill {
				continue
			}
			for _, m := range group {
				if review.Candidates[i].EntryID == m.ID {
					review.Candidates[i].Confidence = 0.8
					review.Candidates[i].Reason = "the same pattern appears in " +
						itoa(len(group)) + " runs; a reusable skill is likely"
					review.Candidates[i].Evidence = "runs: " + itoa(len(group))
				}
			}
		}
	}

	review.Candidates = core.SortedCandidates(review.Candidates)
	if len(review.Candidates) > cfg.MaxCandidates {
		review.Candidates = review.Candidates[:cfg.MaxCandidates]
	}
	return review
}

// patternKey normalises a memory's text so two descriptions of one pattern
// group together.
//
// Lowercasing and collapsing whitespace catches restatement and nothing more.
// That is deliberate: a looser key would merge different lessons, and the
// proposal it produced would point a reviewer at the wrong material.
func patternKey(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(content)), " ")
}

// hasReusablePattern reports whether a memory reads like a procedure rather
// than an incident.
//
// The signals are imperative verbs at the START of a clause and enumerated
// steps. Position matters, and the first version of this function got it wrong:
// matching "run " anywhere made "the run went fine" look like a procedure,
// because "run" is both the verb and the noun this system uses for everything.
// That is the fourth time a substring check has produced a false positive here
// (tool names, language names), so the fix is the same one: require the match to
// be where it means something.
//
// The rule: an instruction begins with a verb, or is a numbered step. An
// incident report begins with its subject ("the deploy failed", "we saw").
func hasReusablePattern(content string) bool {
	lowered := strings.ToLower(strings.TrimSpace(content))
	if lowered == "" {
		return false
	}

	// A clause that STARTS with an imperative verb is an instruction. A leading
	// sequencer ("first", "then") is skipped first, because "first run the
	// migration" is an instruction whose verb is not at position zero.
	imperatives := []string{
		"run ", "use ", "set ", "install ", "call ", "configure ", "add ", "create ",
		"always ", "never ", "ensure ", "check ", "prefer ", "avoid ", "make sure ",
	}
	sequencers := []string{"first ", "then ", "next ", "finally ", "after that ", "before "}
	for _, clause := range splitClauses(lowered) {
		body := clause
		for _, seq := range sequencers {
			if strings.HasPrefix(body, seq) {
				body = strings.TrimSpace(strings.TrimPrefix(body, seq))
				break
			}
		}
		for _, verb := range imperatives {
			if strings.HasPrefix(body, verb) {
				return true
			}
		}
		// An enumerated step is an instruction regardless of its verb.
		if hasStepPrefix(clause) {
			return true
		}
	}
	return false
}

// splitClauses breaks text at sentence and clause boundaries, so an imperative
// later in a memory still counts.
func splitClauses(lowered string) []string {
	fields := strings.FieldsFunc(lowered, func(r rune) bool {
		switch r {
		case '.', ';', '\n', ',', '!', '?':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// hasStepPrefix reports whether a clause is a numbered or lettered step.
func hasStepPrefix(clause string) bool {
	if strings.HasPrefix(clause, "step ") {
		return true
	}
	// "1." / "1)" / "2:" — a number followed by a step marker.
	digits := 0
	for _, r := range clause {
		if r >= '0' && r <= '9' {
			digits++
			continue
		}
		if digits > 0 && (r == '.' || r == ')' || r == ':') {
			return true
		}
		return false
	}
	return false
}

// describeAssertions names the kinds found, for a one-line reason.
func describeAssertions(assertions []core.Assertion) string {
	kinds := map[string]bool{}
	var order []string
	for _, a := range assertions {
		if !kinds[a.Kind] {
			kinds[a.Kind] = true
			order = append(order, a.Kind)
		}
	}
	return strings.Join(order, ", ")
}

// assertionValues renders the found values compactly for the evidence field.
func assertionValues(assertions []core.Assertion) string {
	parts := make([]string, 0, len(assertions))
	for _, a := range assertions {
		parts = append(parts, a.Kind+"="+a.Value)
	}
	return strings.Join(parts, " ")
}

// itoa is a local helper so reason strings do not pull in strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
