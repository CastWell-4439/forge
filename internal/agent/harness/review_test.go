package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// reviewMem builds a memory for review.
func reviewMem(id, content string, layer core.MemoryLayer, age time.Duration) core.MemoryEntry {
	at := time.Now().Add(-age)
	return core.MemoryEntry{
		ID:         id,
		Content:    content,
		Category:   "experience",
		Layer:      layer,
		Source:     core.MemorySource("run:" + id + "-run"),
		ObservedAt: at,
		CreatedAt:  at,
	}
}

// --- freshness ---

// A fresh episodic memory is not proposed for anything: its value is highest
// right after the run that produced it, and acting early would discard
// something still in use.
func TestFreshEpisodicIsNotProposed(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "the deploy failed because the token expired", core.LayerEpisodic, time.Hour),
		},
	}, nil, DefaultReviewConfig())

	assert.Empty(t, review.Candidates)
	assert.Equal(t, 1, review.EpisodicChecked)
}

// --- routing by content ---

// An old episodic memory describing a procedure is proposed for distillation:
// it is the raw material a skill would encode.
func TestOldProcedureProposesDistill(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "to deploy, first run the migration then restart the api", core.LayerEpisodic, 60*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1)
	assert.Equal(t, core.CandidateDistill, review.Candidates[0].Kind)
	assert.Contains(t, review.Candidates[0].Reason, "repeatable procedure")
}

// An old episodic memory that states a checkable claim is proposed for
// promotion: it may be a durable fact rather than an anecdote.
func TestOldClaimProposesPromote(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "the project is written in rust", core.LayerEpisodic, 60*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1)
	assert.Equal(t, core.CandidatePromote, review.Candidates[0].Kind)
	assert.Contains(t, review.Candidates[0].Reason, "language")
	assert.Contains(t, review.Candidates[0].Evidence, "language=rust")
}

// An old episodic memory that states nothing checkable and describes no
// procedure has no destination, and the review says so. This is the rule that
// makes "nothing checkable" actionable rather than merely unfortunate.
func TestOldVagueMemoryProposesDiscard(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "the run went fine", core.LayerEpisodic, 60*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1)
	assert.Equal(t, core.CandidateDiscard, review.Candidates[0].Kind)
	assert.Contains(t, review.Candidates[0].Reason, "nothing checkable")
}

// --- facts ---

// A fact stating an extractable claim is proposed for verification — the review
// names what to check; checking is the next round's work.
func TestFactProposesVerify(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "production runs on v2.1 at api.example.com", core.LayerFact, time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1)
	assert.Equal(t, core.CandidateVerify, review.Candidates[0].Kind)
	assert.Contains(t, review.Candidates[0].Reason, "check it against recorded run evidence")
	assert.Equal(t, 1, review.FactsChecked)
	assert.Equal(t, 0, review.EpisodicChecked)
}

// A fact with nothing extractable produces no proposal: there is nothing to
// check, and proposing a check with no subject wastes a reviewer's time.
func TestFactWithoutClaimIsNotProposed(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "things generally work out", core.LayerFact, time.Hour),
		},
	}, nil, DefaultReviewConfig())

	assert.Empty(t, review.Candidates)
	assert.Equal(t, 1, review.FactsChecked)
}

// A fact is never proposed for discard however old it is: facts are the layer
// that is supposed to persist, and age is not evidence against a claim.
func TestOldFactIsNeverDiscarded(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "the project is written in go", core.LayerFact, 1000*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	for _, c := range review.Candidates {
		assert.NotEqual(t, core.CandidateDiscard, c.Kind, "a fact is checked, not discarded for age")
	}
}

// --- pattern agreement ---

// The same pattern from several runs raises the distillation proposal: that is
// the difference between an anecdote and a reusable lesson.
func TestRepeatedPatternRaisesDistillationConfidence(t *testing.T) {
	content := "to deploy, first run the migration then restart the api"
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", content, core.LayerEpisodic, 60*24*time.Hour),
			reviewMem("e2", content, core.LayerEpisodic, 61*24*time.Hour),
			reviewMem("e3", content, core.LayerEpisodic, 62*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 3)
	for _, c := range review.Candidates {
		assert.Equal(t, core.CandidateDistill, c.Kind)
		assert.GreaterOrEqual(t, c.Confidence, 0.8, "agreement raises the proposal")
		assert.Contains(t, c.Reason, "3 runs")
	}
}

// A single occurrence keeps the base confidence: one run is an anecdote.
func TestSingleOccurrenceKeepsBaseConfidence(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "to deploy, run the migration", core.LayerEpisodic, 60*24*time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1)
	assert.InDelta(t, 0.6, review.Candidates[0].Confidence, 1e-9)
}

// --- the injected model pass ---

// fakeExtractor stands in for a model.
type fakeExtractor struct {
	found []core.Assertion
	err   error
	calls int
}

func (f *fakeExtractor) ExtractAssertions(context.Context, string) ([]core.Assertion, error) {
	f.calls++
	return f.found, f.err
}

// The model pass adds findings and marks them as its own, so a reviewer can
// tell a deterministic finding from a suggested one.
func TestModelPassAddsAssertions(t *testing.T) {
	extractor := &fakeExtractor{found: []core.Assertion{
		{Kind: "service", Value: "internal-mesh", Source: core.ExtractionModel},
	}}

	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "the project is written in go", core.LayerFact, time.Hour),
		},
	}, extractor, DefaultReviewConfig())

	assert.Equal(t, 1, extractor.calls)
	kinds := map[string]string{}
	for _, a := range review.AssertionsFound {
		kinds[a.Kind+":"+a.Value] = a.Source
	}
	assert.Equal(t, core.ExtractionRule, kinds["language:go"], "the static finding keeps its origin")
	assert.Equal(t, core.ExtractionModel, kinds["service:internal-mesh"], "and the model finding keeps its own")
}

// A failing model degrades to the static pass: the static rules are the floor,
// not a fallback of last resort.
func TestModelFailureFallsBackToStatic(t *testing.T) {
	extractor := &fakeExtractor{err: errors.New("model unavailable")}

	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "the project is written in go", core.LayerFact, time.Hour),
		},
	}, extractor, DefaultReviewConfig())

	require.Len(t, review.Candidates, 1, "the review still produced a proposal")
	assert.Contains(t, review.Candidates[0].Evidence, "language=go", "from the static pass")
}

// A nil extractor is the normal local case and must be safe.
func TestNoExtractorIsSafe(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("f1", "written in go", core.LayerFact, time.Hour),
		},
	}, nil, DefaultReviewConfig())
	assert.Len(t, review.Candidates, 1)
}

// --- the review proposes, it does not act ---

// This is the property the whole design rests on: the review's output is data.
// Nothing in the returned value carries an instruction, a mutation or a
// callback — a candidate is a proposal a human reads.
func TestReviewReturnsOnlyProposals(t *testing.T) {
	review := ReviewMemories(context.Background(), ReviewInput{
		Memories: []core.MemoryEntry{
			reviewMem("e1", "to deploy, run the migration", core.LayerEpisodic, 60*24*time.Hour),
			reviewMem("e2", "the run went fine", core.LayerEpisodic, 60*24*time.Hour),
			reviewMem("f1", "written in go", core.LayerFact, time.Hour),
		},
	}, nil, DefaultReviewConfig())

	require.Len(t, review.Candidates, 3)
	for _, c := range review.Candidates {
		assert.NotEmpty(t, c.EntryID)
		assert.NotEmpty(t, c.Kind)
		assert.NotEmpty(t, c.Reason, "every proposal explains itself")
		// The input memories are unchanged: a review does not edit what it reads.
	}
}

// The input slice is not mutated by the review.
func TestReviewDoesNotMutateInput(t *testing.T) {
	memories := []core.MemoryEntry{
		reviewMem("e1", "the run went fine", core.LayerEpisodic, 60*24*time.Hour),
	}
	before := memories[0]

	_ = ReviewMemories(context.Background(), ReviewInput{Memories: memories}, nil, DefaultReviewConfig())

	assert.Equal(t, before, memories[0], "a review reads; it does not edit")
}

// --- config ---

func TestReviewConfigDefaults(t *testing.T) {
	cfg := ReviewConfig{}.normalize()
	d := DefaultReviewConfig()
	assert.Equal(t, d.EpisodicTTL, cfg.EpisodicTTL)
	assert.Equal(t, d.DistillMinRuns, cfg.DistillMinRuns)
	assert.Equal(t, d.MaxCandidates, cfg.MaxCandidates)
	assert.False(t, cfg.Now.IsZero(), "Now is filled so age is computable")

	// A negative TTL must not make everything look expired.
	cfg = ReviewConfig{EpisodicTTL: -time.Hour}.normalize()
	assert.Equal(t, d.EpisodicTTL, cfg.EpisodicTTL)
}

// The candidate cap applies: a long list is capped, because a reviewer can
// re-run the review with different thresholds. (A prompt budget could not do
// this, which is why conflicts are exempt from THAT cap.)
func TestCandidateCap(t *testing.T) {
	var memories []core.MemoryEntry
	for i := 0; i < 30; i++ {
		memories = append(memories, reviewMem(
			"e"+itoa(i), "the run went fine", core.LayerEpisodic, 60*24*time.Hour))
	}

	cfg := DefaultReviewConfig()
	cfg.MaxCandidates = 5
	review := ReviewMemories(context.Background(), ReviewInput{Memories: memories}, nil, cfg)
	assert.Len(t, review.Candidates, 5)
}

// --- helpers ---

func TestPatternKeyGroupsRestatement(t *testing.T) {
	assert.Equal(t, patternKey("The  Run went FINE"), patternKey("the run went fine"))
	assert.NotEqual(t, patternKey("the run went fine"), patternKey("the run failed"))
}

func TestHasReusablePattern(t *testing.T) {
	assert.True(t, hasReusablePattern("to deploy, run the migration"))
	assert.True(t, hasReusablePattern("Always check the token before deploying"))
	assert.True(t, hasReusablePattern("Step 1: clone the repository"))

	// An incident report is not a procedure.
	assert.False(t, hasReusablePattern("the deploy failed because the token expired"))
	assert.False(t, hasReusablePattern("we saw a timeout"))
}

func TestDescribeAndRenderAssertions(t *testing.T) {
	assertions := []core.Assertion{
		{Kind: "language", Value: "go"},
		{Kind: "language", Value: "rust"},
		{Kind: "path", Value: "go"},
	}
	// Kinds are named once even when several values share one.
	assert.Equal(t, "language, path", describeAssertions(assertions))
	assert.Equal(t, "language=go language=rust path=go", assertionValues(assertions))
}

var _ = strings.TrimSpace
