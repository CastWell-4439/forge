package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// fakeLessons is a scripted core.LessonSource.
type fakeLessons struct {
	items []core.RecallItem
	err   error
	// gotQuery/gotTopK record what the loop asked for.
	gotQuery string
	gotTopK  int
}

func (f *fakeLessons) Recall(_ context.Context, query string, topK int) ([]core.RecallItem, error) {
	f.gotQuery, f.gotTopK = query, topK
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// The channel's whole point: a fresh run starts with the lessons in view, and
// each line carries its provenance so the model can weigh the claim.
func TestLessonRecallEntersThePrompt(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	source := &fakeLessons{items: []core.RecallItem{{
		ID:          "lesson-1",
		Title:       "Retry after a failed publish",
		Category:    "reliability",
		Content:     "the publish gate rejects a pack whose readme is still the template",
		SourceRunID: "run-abc",
	}}}
	loop.SetLessonSource(source)

	result, err := loop.Run(context.Background(), "session-1", "publish a skill pack")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	first := joinedText(llm.calls[0])
	assert.Contains(t, first, "Lessons from earlier runs", "the block was injected")
	assert.Contains(t, first, "Retry after a failed publish")
	assert.Contains(t, first, "reliability", "the category is shown")
	assert.Contains(t, first, "from run run-abc", "provenance travels with the lesson")
	assert.Contains(t, first, "verify against the current state",
		"the block tells the model to re-verify: a lesson is history, not current state")

	// The query the source saw is the user's task, and the ask is bounded.
	assert.Equal(t, "publish a skill pack", source.gotQuery)
	assert.Equal(t, defaultLessonTopK, source.gotTopK)
}

// F3-6: a lesson derived FROM this very run must never be fed back into it.
// That is how a system starts teaching itself its own mistakes.
func TestLessonRecallDropsSelfProducedLessons(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	loop.SetLessonSource(&fakeLessons{items: []core.RecallItem{
		{ID: "self", Title: "my own verdict", Content: "this run went badly", SourceRunID: "session-self"},
		{ID: "other", Title: "someone else's", Content: "a different run learned this", SourceRunID: "run-other"},
	}})

	_, err := loop.Run(context.Background(), "session-self", "do the thing")
	require.NoError(t, err)

	prompt := joinedText(llm.calls[0])
	assert.NotContains(t, prompt, "my own verdict", "a lesson from this run is filtered out")
	assert.NotContains(t, prompt, "this run went badly")
	assert.Contains(t, prompt, "someone else's", "other runs' lessons still arrive")
}

// A filter can be replaced (the hook the author asked to be symmetric with
// MemoryWriteJudge). When it rejects everything, no header is left behind.
func TestLessonFilterCanRejectEverything(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetLessonSource(&fakeLessons{items: []core.RecallItem{
		{ID: "1", Content: "anything at all"},
	}})
	loop.SetLessonFilter(func(context.Context, string, core.RecallItem) bool { return false })

	_, err := loop.Run(context.Background(), "s", "task")
	require.NoError(t, err)
	prompt := joinedText(llm.calls[0])
	assert.NotContains(t, prompt, "Lessons from earlier runs",
		"an all-filtered result leaves no empty header in the prompt")
	assert.NotContains(t, prompt, "anything at all")
}

// F3-8: an outage in the lessons store is an enhancement outage, not a run
// failure — the run proceeds exactly as it would have without the channel.
func TestLessonRecallFailureDoesNotFailTheRun(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetLessonSource(&fakeLessons{err: errors.New("index is corrupt")})

	result, err := loop.Run(context.Background(), "s", "task")
	require.NoError(t, err, "a broken lessons index must not stop the run")
	assert.Equal(t, "completed", result.Reason)
	assert.NotContains(t, joinedText(llm.calls[0]), "Lessons from earlier runs")
}

// An empty result is normal (nothing learned yet), not a header with nothing
// under it.
func TestLessonRecallEmptyResultAddsNothing(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetLessonSource(&fakeLessons{})

	_, err := loop.Run(context.Background(), "s", "task")
	require.NoError(t, err)
	assert.NotContains(t, joinedText(llm.calls[0]), "Lessons from earlier runs")
}

// No source installed = the historical behaviour, unchanged.
func TestNoLessonSourceKeepsHistoricalBehaviour(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())

	_, err := loop.Run(context.Background(), "s", "task")
	require.NoError(t, err)
	assert.NotContains(t, joinedText(llm.calls[0]), "Lessons")
}

// A resumed run already carries its history: lessons are for fresh starts.
func TestLessonRecallSkippedOnResume(t *testing.T) {
	store := newMemoryStore()
	require.NoError(t, store.Save(context.Background(), &core.Checkpoint{
		ID: "resume-1", SessionID: "resume-1", StepIndex: 0,
		Messages: []core.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "go"}},
	}))

	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetCheckpoint(store)
	source := &fakeLessons{items: []core.RecallItem{{ID: "1", Content: "old learning"}}}
	loop.SetLessonSource(source)

	_, err := loop.Resume(context.Background(), "resume-1")
	require.NoError(t, err)
	assert.Empty(t, source.gotQuery, "a resumed run does not re-recall lessons")
	assert.NotContains(t, joinedText(llm.calls[0]), "old learning")
}

// The default filter's own rules, tested directly: empty content is dropped,
// the self run is dropped, everything else is admitted.
func TestDefaultLessonFilterRules(t *testing.T) {
	ctx := context.Background()
	assert.False(t, defaultLessonFilter(ctx, "s", core.RecallItem{}), "nothing to say")
	assert.False(t, defaultLessonFilter(ctx, "s", core.RecallItem{Content: "   "}), "whitespace only")
	assert.False(t, defaultLessonFilter(ctx, "s", core.RecallItem{Content: "x", SourceRunID: "s"}),
		"produced by this run")
	assert.True(t, defaultLessonFilter(ctx, "s", core.RecallItem{Content: "x", SourceRunID: "other"}))
	assert.True(t, defaultLessonFilter(ctx, "s", core.RecallItem{Content: "x"}),
		"no provenance (synthetic lesson) is still admitted")
	assert.True(t, defaultLessonFilter(ctx, "", core.RecallItem{Content: "x", SourceRunID: "any"}),
		"an unknown session cannot claim self-authorship")
	// A title alone is enough to be worth showing.
	assert.True(t, defaultLessonFilter(ctx, "s", core.RecallItem{Title: "has a title"}))
}

// The block is bounded: a long lesson is truncated rather than pasted whole.
func TestLessonRecallTruncatesLongContent(t *testing.T) {
	llm := &capturingLLM{responses: []string{finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()), DefaultLoopConfig())
	loop.SetLessonSource(&fakeLessons{items: []core.RecallItem{{
		ID:      "long",
		Title:   "verbose",
		Content: strings.Repeat("detail ", 500),
	}}})

	_, err := loop.Run(context.Background(), "s", "task")
	require.NoError(t, err)
	prompt := joinedText(llm.calls[0])
	assert.Contains(t, prompt, "verbose")
	assert.Less(t, strings.Count(prompt, "detail"), 200, "the lesson was truncated for the prompt")
}
