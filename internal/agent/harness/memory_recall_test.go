package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

// countingMemory implements core.MemoryStore and records what the loop did.
type countingMemory struct {
	mu        sync.Mutex
	saved     []core.MemoryEntry
	searchErr error
	recalls   int
}

func (m *countingMemory) SaveShortTerm(context.Context, string, string, any) error { return nil }
func (m *countingMemory) GetShortTerm(context.Context, string, string) (any, error) {
	return nil, nil
}

func (m *countingMemory) SaveLongTerm(_ context.Context, entry core.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, entry)
	return nil
}

func (m *countingMemory) SearchLongTerm(_ context.Context, _ string, _ int) ([]core.MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recalls++
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return []core.MemoryEntry{{
		ID:       "mem-1",
		Content:  "previous run found the release checklist in docs/",
		Category: "experience",
	}}, nil
}

func (m *countingMemory) saveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

// newMemoryLoop builds a loop with one registered tool and the given store.
func newMemoryLoop(t *testing.T, llm core.LLMClient, mem core.MemoryStore) *AgentLoop {
	t.Helper()
	registry, _ := countingRegistry(t, "file.read", true)
	loop := NewAgentLoop(llm, NewToolRouter(registry), DefaultLoopConfig())
	loop.SetMemory(mem)
	return loop
}

// TestMemoryRecallEntersTheContext is the read side of F1: SearchLongTerm used
// to have no non-test caller at all. The recalled experience must sit between
// the system prompt and the user turn.
func TestMemoryRecallEntersTheContext(t *testing.T) {
	mem := &countingMemory{}
	llm := &capturingLLM{responses: []string{`{"thought": "t", "answer": "done"}`}}
	loop := newMemoryLoop(t, llm, mem)

	if _, err := loop.Run(context.Background(), "s1", "find the release checklist"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if mem.recalls != 1 {
		t.Fatalf("SearchLongTerm calls = %d, want 1", mem.recalls)
	}
	if len(llm.calls) == 0 {
		t.Fatal("no llm call recorded")
	}
	msgs := llm.calls[0]
	if len(msgs) < 3 {
		t.Fatalf("messages = %d, want system + recall + user", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[1].Role != "system" || msgs[2].Role != "user" {
		t.Errorf("order = %s/%s/%s, want system/system/user", msgs[0].Role, msgs[1].Role, msgs[2].Role)
	}
	if !strings.Contains(msgs[1].Content, "release checklist") {
		t.Errorf("recall block = %q, want the stored experience", msgs[1].Content)
	}
}

// TestMemoryRecallFailureIsTolerated: memory is an enhancement; a broken index
// must not stop the run.
func TestMemoryRecallFailureIsTolerated(t *testing.T) {
	mem := &countingMemory{searchErr: errors.New("index corrupted")}
	llm := &capturingLLM{responses: []string{`{"thought": "t", "answer": "ok"}`}}
	loop := newMemoryLoop(t, llm, mem)

	result, err := loop.Run(context.Background(), "s1", "anything")
	if err != nil {
		t.Fatalf("recall failure must not fail the run: %v", err)
	}
	if result.Reason != "completed" {
		t.Errorf("reason = %s", result.Reason)
	}
	if len(llm.calls) != 1 || len(llm.calls[0]) != 2 {
		t.Errorf("recall block should be absent on error, calls=%v", llm.calls)
	}
}

// TestMemoryWriteJudgeDefault is the write side of F1: a one-shot answer is not
// experience, and skipping it also skips the lesson-extraction LLM call that
// used to be paid every time.
func TestMemoryWriteJudgeDefault(t *testing.T) {
	ctx := context.Background()

	t.Run("completed with tools is remembered", func(t *testing.T) {
		mem := &countingMemory{}
		llm := &capturingLLM{responses: []string{
			`{"thought": "t", "action": {"name": "file.read", "params": {"path": "a"}}}`,
			`{"thought": "t", "answer": "done"}`,
			`{"thought": "extract", "answer": "lesson text"}`, // the extraction call
		}}
		loop := newMemoryLoop(t, llm, mem)

		if _, err := loop.Run(ctx, "s", "do it with tools"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if mem.saveCount() != 1 {
			t.Errorf("saved = %d, want 1", mem.saveCount())
		}
	})

	t.Run("one-shot answer is not remembered and costs no extra call", func(t *testing.T) {
		mem := &countingMemory{}
		llm := &capturingLLM{responses: []string{`{"thought": "t", "answer": "42"}`}}
		loop := newMemoryLoop(t, llm, mem)

		if _, err := loop.Run(ctx, "s", "what is 6*7"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if mem.saveCount() != 0 {
			t.Errorf("saved = %d, want 0 for a run with no tool steps", mem.saveCount())
		}
		if len(llm.calls) != 1 {
			t.Errorf("llm calls = %d, want 1 (no lesson-extraction call)", len(llm.calls))
		}
	})
}

// TestMemoryWriteJudgeCustomHonoured: an injected judge (an LLM judge would be
// assembled the same way) overrides the default.
func TestMemoryWriteJudgeCustomHonoured(t *testing.T) {
	mem := &countingMemory{}
	llm := &capturingLLM{responses: []string{
		`{"thought": "t", "action": {"name": "file.read", "params": {"path": "a"}}}`,
		`{"thought": "t", "answer": "done"}`,
		`{"thought": "extract", "answer": "unused"}`,
	}}
	loop := newMemoryLoop(t, llm, mem)
	loop.SetMemoryWriteJudge(func(context.Context, string, *RunResult) bool { return false })

	if _, err := loop.Run(context.Background(), "s", "task"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if mem.saveCount() != 0 {
		t.Errorf("saved = %d, want 0 (judge said no)", mem.saveCount())
	}
	if len(llm.calls) != 2 {
		t.Errorf("llm calls = %d, want 2 (judge rejected before extraction)", len(llm.calls))
	}
}
