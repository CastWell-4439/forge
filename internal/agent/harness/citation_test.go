package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// --- citation resolution ---

// A cited id that was actually recalled is evidence; anything else is not.
func TestResolveCitationsKeepsOnlyRecalledIDs(t *testing.T) {
	recalled := map[string]bool{"mem_1": true, "mem_2": true}

	result := resolveCitations([]string{"mem_1", "mem_2"}, recalled)
	assert.True(t, result.Reported)
	assert.True(t, result.UsageKnown())
	assert.ElementsMatch(t, []string{"mem_1", "mem_2"}, result.Known)
	assert.Empty(t, result.Unknown)
}

// A model can invent an id. An invented citation is a fabricated fact, and
// accepting it would let a hallucination steer the archive.
func TestResolveCitationsDropsUnknownIDs(t *testing.T) {
	recalled := map[string]bool{"mem_1": true}

	result := resolveCitations([]string{"mem_1", "mem_999", "made-up"}, recalled)
	assert.True(t, result.UsageKnown(), "one real citation is still evidence")
	assert.Equal(t, []string{"mem_1"}, result.Known)
	assert.ElementsMatch(t, []string{"mem_999", "made-up"}, result.Unknown)
}

// Citing only ids that were never shown is NOT evidence of use — and equally
// not evidence of disuse. It resolves to nothing.
func TestResolveCitationsAllUnknownIsNotEvidence(t *testing.T) {
	recalled := map[string]bool{"mem_1": true}

	result := resolveCitations([]string{"ghost"}, recalled)
	assert.True(t, result.Reported, "the model did say something")
	assert.False(t, result.UsageKnown(), "but nothing it said could be verified")
	assert.Empty(t, result.Known)
	assert.Equal(t, []string{"ghost"}, result.Unknown)
}

// Silence produces no signal at all. This is the rule the whole lifecycle rests
// on: reading silence as "declined" would archive every memory an agent saw.
func TestSilenceIsNotDisuse(t *testing.T) {
	recalled := map[string]bool{"mem_1": true}

	result := resolveCitations(nil, recalled)
	assert.False(t, result.Reported)
	assert.False(t, result.UsageKnown())
	assert.Empty(t, result.Known)
	assert.Empty(t, result.Unknown)
}

// Blank entries and duplicates are dropped: a model that emits an empty string
// or repeats itself must not create phantom evidence.
func TestResolveCitationsNormalizes(t *testing.T) {
	recalled := map[string]bool{"mem_1": true}

	result := resolveCitations([]string{"mem_1", " mem_1 ", "", "   "}, recalled)
	assert.Equal(t, []string{"mem_1"}, result.Known, "trimmed and deduplicated")
	assert.Empty(t, result.Unknown)
}

// --- observations from citations ---

// A verified citation becomes Used=true with UsageKnown=true: the single
// combination the contract can establish.
func TestUsageObservationsFromVerifiedCitations(t *testing.T) {
	now := time.Now().UTC()
	obs := usageObservations("run-1", CitationResult{Known: []string{"mem_1", "mem_2"}}, now)

	require.Len(t, obs, 2)
	for _, o := range obs {
		assert.Equal(t, "run-1", o.RunID)
		assert.Equal(t, core.KindMemory, o.Kind)
		assert.True(t, o.Used)
		assert.True(t, o.UsageKnown, "a verified citation IS a usable signal")
		assert.Equal(t, now, o.At)
	}
}

// No verified citations means no observations — not an observation of disuse.
func TestNoObservationsWithoutVerifiedCitations(t *testing.T) {
	now := time.Now().UTC()
	assert.Empty(t, usageObservations("run-1", CitationResult{}, now))
	assert.Empty(t, usageObservations("run-1", CitationResult{Reported: true, Unknown: []string{"ghost"}}, now))
	assert.Empty(t, usageObservations("", CitationResult{Known: []string{"mem_1"}}, now),
		"no run id, nothing to attribute")
}

// --- recalled id collection ---

func TestRecalledIDsFromEvidence(t *testing.T) {
	evidence := core.MemoryEvidence{Groups: []core.EvidenceGroup{
		{Members: []core.MemoryEntry{{ID: "mem_1"}, {ID: "mem_2"}}},
		{Members: []core.MemoryEntry{{ID: "mem_3"}, {ID: ""}}},
	}}
	ids := recalledIDs(evidence)
	assert.Len(t, ids, 3, "empty ids are skipped")
	assert.True(t, ids["mem_1"])
	assert.True(t, ids["mem_3"])
}

// --- recording through the loop ---

// recordingSink captures what the loop hands over.
type recordingSink struct {
	runs  []string
	ids   [][]string
	err   error
	calls int
}

func (s *recordingSink) RecordUsage(_ context.Context, runID string, entryIDs []string) error {
	s.calls++
	s.runs = append(s.runs, runID)
	s.ids = append(s.ids, entryIDs)
	return s.err
}

// The loop records only verified citations, and only when a sink is attached.
func TestLoopRecordsVerifiedCitations(t *testing.T) {
	sink := &recordingSink{}
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), DefaultLoopConfig())
	loop.SetUsageSink(sink)

	// Stand in for a recall: one id was offered, one was not.
	loop.recalledThis = map[string]bool{"mem_1": true}
	loop.citedThis = []string{"mem_1", "ghost"}

	loop.recordCitations(t.Context(), "run-1")

	require.Equal(t, 1, sink.calls)
	assert.Equal(t, "run-1", sink.runs[0])
	assert.Equal(t, []string{"mem_1"}, sink.ids[0], "the fabricated id is not recorded")
}

// With no sink the loop resolves and discards: the feature is off, and it costs
// nothing.
func TestLoopWithoutSinkRecordsNothing(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), DefaultLoopConfig())
	loop.recalledThis = map[string]bool{"mem_1": true}
	loop.citedThis = []string{"mem_1"}

	// No panic, no side effect.
	loop.recordCitations(t.Context(), "run-1")
}

// A run that cited nothing verifiable never reaches the sink — the lifecycle
// must not receive a signal it cannot justify.
func TestLoopDoesNotRecordUnverifiableCitations(t *testing.T) {
	sink := &recordingSink{}
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), DefaultLoopConfig())
	loop.SetUsageSink(sink)

	loop.recalledThis = map[string]bool{"mem_1": true}
	loop.citedThis = []string{"ghost"}

	loop.recordCitations(t.Context(), "run-1")
	assert.Zero(t, sink.calls, "nothing verifiable, nothing recorded")
}

// A sink failure is swallowed: bookkeeping must not fail a run that already
// produced its answer.
func TestLoopSurvivesSinkFailure(t *testing.T) {
	sink := &recordingSink{err: assert.AnError}
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(registryWithTools(t, "file.read")), DefaultLoopConfig())
	loop.SetUsageSink(sink)
	loop.recalledThis = map[string]bool{"mem_1": true}
	loop.citedThis = []string{"mem_1"}

	assert.NotPanics(t, func() { loop.recordCitations(t.Context(), "run-1") })
	assert.Equal(t, 1, sink.calls)
}

// --- the prompt contract ---

// Every rendered entry carries its id, or nothing can be cited.
func TestRenderEvidenceCarriesIDs(t *testing.T) {
	now := time.Now()
	evidence := core.MemoryEvidence{Groups: []core.EvidenceGroup{{
		Subject:    "the deploy needs two approvals",
		Relation:   core.RelationComplementary,
		Confidence: 0.6,
		Members: []core.MemoryEntry{{
			ID: "mem_42", Content: "the deploy needs two approvals", Source: "run:a", CreatedAt: now,
		}},
	}}}

	out := renderEvidence(evidence)
	assert.Contains(t, out, "[mem_42]", "the id is visible so it can be cited")
	assert.Contains(t, out, "used_memory", "and the contract is explained")
	assert.Contains(t, out, "optional", "as optional, so a model with nothing to report does not invent something")
}

// A conflict renders both sides WITH their ids: citing one side of a
// disagreement is meaningful, and it needs its own id to be possible.
func TestRenderConflictCarriesBothIDs(t *testing.T) {
	now := time.Now()
	evidence := core.MemoryEvidence{Groups: []core.EvidenceGroup{{
		Subject:    "retry limit is 3",
		Relation:   core.RelationConflicting,
		Confidence: 0.7,
		Members: []core.MemoryEntry{
			{ID: "mem_a", Content: "retry limit is 3", Source: "run:a", CreatedAt: now},
			{ID: "mem_b", Content: "retry limit is 5", Source: "run:b", CreatedAt: now},
		},
	}}}

	out := renderEvidence(evidence)
	assert.Contains(t, out, "mem_a")
	assert.Contains(t, out, "mem_b")
	assert.Contains(t, out, "CONFLICTING EVIDENCE")
}

// An entry with no id is labelled rather than rendered as an empty bracket, so
// a reviewer can see that it was uncitable.
func TestRenderNamesMissingID(t *testing.T) {
	now := time.Now()
	evidence := core.MemoryEvidence{Groups: []core.EvidenceGroup{{
		Subject:    "retry limit is 3",
		Relation:   core.RelationConflicting,
		Confidence: 0.7,
		Members: []core.MemoryEntry{
			{ID: "", Content: "retry limit is 3", CreatedAt: now},
			{ID: "mem_b", Content: "retry limit is 5", CreatedAt: now},
		},
	}}}

	out := renderEvidence(evidence)
	assert.Contains(t, out, "no-id")
}

// With nothing citable the contract line is omitted: inviting a citation when
// there is nothing to cite only adds noise.
func TestRenderOmitsContractWhenNothingCitable(t *testing.T) {
	now := time.Now()
	evidence := core.MemoryEvidence{Groups: []core.EvidenceGroup{{
		Subject:  "uncitable",
		Relation: core.RelationComplementary,
		Members:  []core.MemoryEntry{{ID: "", Content: "uncitable", CreatedAt: now}},
	}}}

	out := renderEvidence(evidence)
	assert.NotContains(t, out, "used_memory")
}
