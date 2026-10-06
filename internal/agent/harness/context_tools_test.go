package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/workers"
)

// --- archive units ---

func TestContextArchiveRecallStatuses(t *testing.T) {
	// A nil archive answers honestly: nothing was ever archived.
	var nilArchive *ContextArchive
	_, status := nilArchive.Recall(context.Background(), "x", 0)
	assert.Equal(t, "no_archive", status)

	a := NewContextArchive(0)
	_, status = a.Recall(context.Background(), "x", 0)
	assert.Equal(t, "no_archive", status, "an empty archive has nothing to search")

	a.Add(2, []core.Message{{Role: "user", Content: "the launch code is BLUE42"}})
	matches, status := a.Recall(context.Background(), "blue42", 5)
	require.Equal(t, "ok", status)
	require.Len(t, matches, 1)
	assert.Equal(t, 2, matches[0].Step, "an excerpt carries the step it came from")
	assert.Contains(t, matches[0].Excerpt, "BLUE42")

	_, status = a.Recall(context.Background(), "nothing-here", 5)
	assert.Equal(t, "no_match", status, "archive exists, genuinely no match")
}

func TestContextArchiveIsBounded(t *testing.T) {
	a := NewContextArchive(3)
	for i := 0; i < 6; i++ {
		a.Add(0, []core.Message{{Role: "user", Content: fmt.Sprintf("msg %d", i)}})
	}
	assert.Equal(t, 3, a.Len(), "the archive must not grow without limit")
	matches, status := a.Recall(context.Background(), "msg", 10)
	require.Equal(t, "ok", status)
	assert.Len(t, matches, 3, "the oldest entries were dropped")
	assert.Contains(t, matches[len(matches)-1].Excerpt, "msg 5", "the newest survived")
}

// --- remaining ---

func TestContextRemainingKnownAndUnknown(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), LoopConfig{MaxContextTokens: 100000})

	msgs := []core.Message{{Role: "user", Content: strings.Repeat("token ", 500)}} // ~125 tokens
	result := loop.runContextTool(context.Background(), "context.remaining", nil, &msgs, "s", 0)
	require.Empty(t, result.Error)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Output), &payload))
	assert.Equal(t, float64(100000), payload["max_tokens"])
	assert.NotZero(t, payload["tokens_used"])
	assert.NotZero(t, payload["tokens_left"])

	// No budget configured: null, never a fabricated figure. NewContextManager
	// clamps 0 to the default, so this is the defensive shape a loop reaches
	// only without a manager budget — unit-tested here, where it matters.
	noBudget := &AgentLoop{ctxMgr: &ContextManager{maxTokens: 0}}
	result = noBudget.runContextTool(context.Background(), "context.remaining", nil, &msgs, "s", 0)
	require.Empty(t, result.Error)
	var nullPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Output), &nullPayload))
	assert.Nil(t, nullPayload["tokens_left"], "an unavailable figure must be null")
	assert.Contains(t, nullPayload["note"], "no context budget")
}

// --- compact + recall round trip ---

func TestContextCompactArchivesThenRecalls(t *testing.T) {
	// summarize() spends one scripted response; nothing else runs.
	llm := &mockLLM{responses: []string{"earlier turns discussed the launch"}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxContextTokens: 100000})

	msgs := []core.Message{{Role: "system", Content: "system rules"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, core.Message{Role: "user",
			Content: fmt.Sprintf("historical turn %d mentioning launch code BLUE42", i)})
	}
	before := len(msgs)

	result := loop.runContextTool(context.Background(), "context.compact", nil, &msgs, "sess", 3)
	require.Empty(t, result.Error, "compaction must succeed on a 6-turn conversation")
	assert.Less(t, len(msgs), before, "the window shrank")
	assert.Contains(t, loop.joinedMessages(msgs), "[Conversation summary:", "the summary replaced the old turns")

	// The archived text is searchable after it left the window.
	recall := loop.runContextTool(context.Background(), "context.recall",
		map[string]any{"query": "BLUE42"}, &msgs, "sess", 3)
	require.Empty(t, recall.Error)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(recall.Output), &payload))
	assert.Equal(t, "ok", payload["status"])
	assert.Contains(t, recall.Output, "BLUE42", "the archived turns are recallable")
	assert.Contains(t, recall.Output, `"step"`, "an excerpt carries its step")

	// The environment contract, spelled out for the model.
	assert.Contains(t, result.Output, "environment state")

	// A second compaction with nothing left to summarise is a report, not a
	// failure: reflexion must not fire on "there was nothing to do".
	msgs2 := []core.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "only one turn"}}
	noop := loop.runContextTool(context.Background(), "context.compact", nil, &msgs2, "sess", 4)
	require.NotEmpty(t, noop.Error, "nothing to compact is an explicit refusal")
	assert.Contains(t, noop.Error, "nothing to compact")
}

// joinedMessages is a test helper: the whole conversation as one string.
func (l *AgentLoop) joinedMessages(msgs []core.Message) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.Role + ":" + m.Content
	}
	return strings.Join(parts, "\n")
}

// --- water lines ---

func TestWaterLineReminderFiresOncePerLevel(t *testing.T) {
	loop := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), LoopConfig{})

	warn := contextUsage{Fraction: 0.75, Known: true}
	assert.Equal(t, "warn", loop.reminderLevel(warn), "first crossing warns")
	assert.Equal(t, "", loop.reminderLevel(warn), "the same level never repeats")

	critical := contextUsage{Fraction: 0.95, Known: true}
	assert.Equal(t, "critical", loop.reminderLevel(critical), "the upper line speaks louder")
	assert.Equal(t, "", loop.reminderLevel(critical), "once per level per run")

	// A critical sighting covers the warning: nothing smaller fires after it.
	fresh := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), LoopConfig{})
	assert.Equal(t, "critical", fresh.reminderLevel(critical))
	assert.Equal(t, "", fresh.reminderLevel(warn))

	// Unknown usage never invents a figure; an operator can turn the whole
	// thing off with a negative threshold.
	assert.Equal(t, "", fresh.reminderLevel(contextUsage{}))
	off := NewAgentLoop(&mockLLM{responses: []string{finalAnswer}},
		NewToolRouter(workers.NewToolRegistry()), LoopConfig{ContextRemindAt: -1})
	assert.Equal(t, "", off.reminderLevel(critical), "reminders can be disabled")
}

// realCalls filters a capturingLLM's record down to the model's own steps:
// the summariser shares the same client (CompactIfNeeded and the model's
// compaction both call Chat), and its prompts are not model steps.
func realCalls(c *capturingLLM) [][]core.Message {
	var out [][]core.Message
	for _, call := range c.calls {
		if strings.Contains(messageText(call), "Summarize the following conversation") {
			continue
		}
		out = append(out, call)
	}
	return out
}

// The loop injects the reminder on the crossing step and never again: the
// second step must still see exactly ONE [context line in its history.
func TestLoopInjectsWaterLineReminderOnce(t *testing.T) {
	llm := &capturingLLM{responses: []string{recallCall("anything"), finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxSteps: 5, SystemPrompt: strings.Repeat("rules of the run. ", 300)})

	// Size the budget from the ACTUAL prompt so usage lands between the two
	// water lines (warn) while the next step still fits: a reminder has to be
	// observable without the static auto-compaction firing and swallowing it.
	probe := EstimateTokens([]core.Message{
		{Role: "system", Content: loop.buildSystemPrompt()},
		{Role: "user", Content: "go"},
	})
	budget := int(float64(probe)/0.8) + 100
	loop.ctxMgr = NewContextManager(budget, llm)

	result, err := loop.Run(testCtx(t), "waterline", "go")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	calls := realCalls(llm)
	require.Len(t, calls, 2, "two model steps ran (no summariser in between)")
	assert.Equal(t, 1, strings.Count(messageText(calls[0]), "[context:"), "injected on the crossing step")
	assert.Equal(t, 1, strings.Count(messageText(calls[1]), "[context:"),
		"still exactly one — each line fires once per run")
	assert.Contains(t, messageText(calls[1]), "context.compact", "the reminder names the action")
}

// joinedText flattens a captured message list for counting.
func joinedText(msgs []core.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// recallCall is a prompt-path tool call for context.recall with its required
// query parameter (a call without it would honestly fail validation).
func recallCall(query string) string {
	return fmt.Sprintf(`{"thought": "recall", "action": {"name": "context.recall", "params": {"query": %q}}}`, query)
}

// testCtx is a context.Background with a testing-friendly name.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// --- interception (prompt path) ---

// The model calls context.recall: the loop must intercept it (the registry is
// EMPTY — if the call fell through to the router it would fail as unregistered),
// record no ledger entry, and put the recall answer in the conversation.
func TestLoopInterceptsContextToolsPromptPath(t *testing.T) {
	llm := &capturingLLM{responses: []string{recallCall("BLUE42"), finalAnswer}}
	store := newMemoryStore()
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxSteps: 5, MaxContextTokens: 100000})
	loop.SetCheckpoint(store)

	result, err := loop.Run(testCtx(t), "intercept", "hi")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	// The observation is the recall answer, not a router failure.
	observed := joinedText(llm.calls[1])
	assert.Contains(t, observed, `"status":"no_archive"`, "the interception answered from the archive")

	// Meta operations never enter the side-effect ledger.
	cp, err := store.Latest(context.Background(), "intercept")
	require.NoError(t, err)
	assert.Empty(t, cp.ToolCalls, "a context tool is not a side effect")

	// Two steps: the call, then the answer.
	require.Len(t, result.Steps, 2)
	assert.Equal(t, "context.recall", result.Steps[0].Action.Name)
}

// --- E5: overflow → compact → retry once ---

// overflowOnceLLM overflows its window on the first run call, then answers.
// Chat (used by the summariser) answers directly: the summariser is part of
// the fix, not the thing that is broken.
type overflowOnceLLM struct {
	calls      [][]core.Message
	chatCalls  int
	overflowed bool
}

func (o *overflowOnceLLM) Chat(_ context.Context, _ []core.Message) (string, error) {
	o.chatCalls++
	return "compact summary of the archived turns", nil
}

func (o *overflowOnceLLM) ChatWithUsage(_ context.Context, messages []core.Message) (core.ChatResult, error) {
	o.calls = append(o.calls, append([]core.Message(nil), messages...))
	if !o.overflowed {
		o.overflowed = true
		return core.ChatResult{}, fmt.Errorf("%w (status 400): provider window too small", errContextOverflow)
	}
	return core.ChatResult{Content: finalAnswer, FinishReason: "stop"}, nil
}

func TestContextOverflowCompactsAndRetriesOnce(t *testing.T) {
	// Resume a long conversation: six turns is more than the keep-tail, so
	// there is something to compact when the provider refuses.
	store := newMemoryStore()
	msgs := []core.Message{{Role: "system", Content: "system rules"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, core.Message{Role: "user",
			Content: fmt.Sprintf("historical turn %d with launch code BLUE42", i)})
	}
	require.NoError(t, store.Save(context.Background(), &core.Checkpoint{
		ID: "e5-session-step-0", SessionID: "e5-session", StepIndex: 0,
		Messages: msgs, CreatedAt: time.Now().UTC(),
	}))

	llm := &overflowOnceLLM{}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxSteps: 5, MaxContextTokens: 100000})
	loop.SetCheckpoint(store)

	result, err := loop.Resume(context.Background(), "e5-session")
	require.NoError(t, err, "the overflow was absorbable: compaction + one retry")
	require.Equal(t, "completed", result.Reason, "the retry landed the answer")
	assert.Equal(t, "finished", result.Answer)

	require.Len(t, llm.calls, 2, "the overflow call and exactly one retry")
	assert.Positive(t, llm.chatCalls, "the summariser ran")
	assert.Less(t, len(llm.calls[1]), len(llm.calls[0]), "the retry carried a compacted window")
	assert.Contains(t, joinedText(llm.calls[1]), "[Conversation summary:",
		"the retry saw the summary, not the raw history")
}

// overflowAlwaysLLM fails the same way every time: one shrink is not enough,
// and a second attempt would loop forever.
type overflowAlwaysLLM struct{ calls int }

func (o *overflowAlwaysLLM) Chat(_ context.Context, _ []core.Message) (string, error) {
	return "summary", nil
}

func (o *overflowAlwaysLLM) ChatWithUsage(_ context.Context, _ []core.Message) (core.ChatResult, error) {
	o.calls++
	return core.ChatResult{}, fmt.Errorf("%w (status 400)", errContextOverflow)
}

func TestContextOverflowGivesUpAfterOneRetry(t *testing.T) {
	store := newMemoryStore()
	msgs := []core.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, core.Message{Role: "user", Content: fmt.Sprintf("turn %d", i)})
	}
	require.NoError(t, store.Save(context.Background(), &core.Checkpoint{
		ID: "e5b-step-0", SessionID: "e5b", StepIndex: 0,
		Messages: msgs, CreatedAt: time.Now().UTC(),
	}))

	llm := &overflowAlwaysLLM{}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxSteps: 5, MaxContextTokens: 100000})
	loop.SetCheckpoint(store)

	result, err := loop.Resume(context.Background(), "e5b")
	require.Error(t, err, "a second overflow surfaces instead of looping")
	assert.True(t, errors.Is(err, errContextOverflow), "the caller sees the real cause: %v", err)
	assert.Equal(t, "error", result.Reason)
	assert.Equal(t, 2, llm.calls, "attempt, compacted retry, stop")
}

// --- journal rebuild after a model-initiated compaction ---

// The compaction event carries the replacement snapshot; the step delta that
// follows must be only the observation. A rebuild therefore reconstructs the
// compacted conversation — no duplication, no lost turn.
func TestRebuildAfterModelCompaction(t *testing.T) {
	store := newMemoryStore()
	msgs := []core.Message{{Role: "system", Content: "system rules"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, core.Message{Role: "user",
			Content: fmt.Sprintf("historical turn %d with launch code BLUE42", i)})
	}
	require.NoError(t, store.Save(context.Background(), &core.Checkpoint{
		ID: "rc-step-0", SessionID: "rc", StepIndex: 0,
		Messages: msgs, CreatedAt: time.Now().UTC(),
	}))

	j := pathJournal(t, t.TempDir())
	llm := &capturingLLM{responses: []string{toolCall("context.compact"), finalAnswer}}
	loop := NewAgentLoop(llm, NewToolRouter(workers.NewToolRegistry()),
		LoopConfig{MaxSteps: 5, MaxContextTokens: 100000})
	loop.SetCheckpoint(store)
	loop.SetJournal(j)

	result, err := loop.Resume(testCtx(t), "rc")
	require.NoError(t, err)
	require.Equal(t, "completed", result.Reason)

	// The run's own second step saw the compacted window (skipping the
	// summariser's prompt, which shares the client record).
	calls := realCalls(llm)
	require.Len(t, calls, 2, "two model steps (summariser prompts filtered)")
	live := messageText(calls[1])
	assert.Contains(t, live, "[Conversation summary:")

	events, err := j.ReadEvents(context.Background(), "rc")
	require.NoError(t, err)
	rebuilt, err := RebuildCheckpoint(events)
	require.NoError(t, err)

	rebuiltText := joinedText(rebuilt.Messages)
	assert.Contains(t, rebuiltText, "[Conversation summary:", "the replacement snapshot rebuilt")
	assert.Contains(t, rebuiltText, `"status":"compacted"`, "the step's observation rebuilt")
	assert.Equal(t, 1, strings.Count(rebuiltText, "[Conversation summary:"),
		"exactly one summary — no duplicated history")
	assert.Equal(t, 1, strings.Count(rebuiltText, "system rules"), "the system prompt did not duplicate")
}
