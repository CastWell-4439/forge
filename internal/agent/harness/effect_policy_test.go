package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

// The four effects E4 covers, each with a fake that fails on demand.

type failingBudget struct{}

func (failingBudget) Check(context.Context, string) error { return nil }
func (failingBudget) Record(context.Context, string, int64) error {
	return errors.New("usage sink unavailable")
}

type failingOutputGuard struct{}

func (failingOutputGuard) Check(context.Context, string) (string, error) {
	return "", errors.New("guard ruleset failed to load")
}

type failingVerifier struct{}

func (failingVerifier) Verify(context.Context, core.ToolCall, *core.ToolResult) (bool, string, error) {
	return false, "", errors.New("verifier backend unreachable")
}

// failingMemory fails only the long-term write; recall returns nothing.
type failingMemory struct{ saves int }

func (f *failingMemory) SaveShortTerm(context.Context, string, string, any) error { return nil }
func (f *failingMemory) GetShortTerm(context.Context, string, string) (any, error) {
	return nil, nil
}
func (f *failingMemory) SaveLongTerm(context.Context, core.MemoryEntry) error {
	f.saves++
	return errors.New("memory store full")
}
func (f *failingMemory) SearchLongTerm(context.Context, string, int) ([]core.MemoryEntry, error) {
	return nil, nil
}

// usageLLM reports token usage so the budget Record path actually fires
// (the loop skips Record when usage is zero).
type usageLLM struct{ content string }

func (u *usageLLM) Chat(ctx context.Context, msgs []core.Message) (string, error) {
	r, err := u.ChatWithUsage(ctx, msgs)
	return r.Content, err
}

func (u *usageLLM) ChatWithUsage(context.Context, []core.Message) (core.ChatResult, error) {
	return core.ChatResult{
		Content: u.content,
		Usage:   core.TokenUsage{TotalTokens: 42},
	}, nil
}

func TestBudgetRecordFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("best effort continues", func(t *testing.T) {
		loop := NewAgentLoop(&usageLLM{content: `{"thought": "t", "answer": "ok"}`},
			NewToolRouter(core.NewToolRegistry()), DefaultLoopConfig())
		loop.SetBudget(failingBudget{})

		result, err := loop.Run(ctx, "s", "q")
		if err != nil {
			t.Fatalf("default must continue: %v", err)
		}
		if result.Reason != "completed" {
			t.Errorf("reason = %s", result.Reason)
		}
	})

	t.Run("strict fails the run", func(t *testing.T) {
		loop := NewAgentLoop(&usageLLM{content: `{"thought": "t", "answer": "ok"}`},
			NewToolRouter(core.NewToolRegistry()), DefaultLoopConfig())
		loop.SetBudget(failingBudget{})
		loop.SetEffectFailurePolicy(EffectStrict)

		if _, err := loop.Run(ctx, "s", "q"); err == nil {
			t.Fatal("strict must surface the budget failure")
		} else if !strings.Contains(err.Error(), "budget record failed") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestOutputGuardFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("best effort continues", func(t *testing.T) {
		loop := NewAgentLoop(&mockLLM{responses: []string{`{"thought": "t", "answer": "raw"}`}},
			NewToolRouter(core.NewToolRegistry()), DefaultLoopConfig())
		loop.SetOutputGuard(failingOutputGuard{})

		result, err := loop.Run(ctx, "s", "q")
		if err != nil {
			t.Fatalf("default must continue: %v", err)
		}
		if result.Answer != "raw" {
			t.Errorf("answer = %q, want the unfiltered one", result.Answer)
		}
	})

	t.Run("strict fails the run", func(t *testing.T) {
		loop := NewAgentLoop(&mockLLM{responses: []string{`{"thought": "t", "answer": "raw"}`}},
			NewToolRouter(core.NewToolRegistry()), DefaultLoopConfig())
		loop.SetOutputGuard(failingOutputGuard{})
		loop.SetEffectFailurePolicy(EffectStrict)

		if _, err := loop.Run(ctx, "s", "q"); err == nil {
			t.Fatal("strict must not ship an unguarded answer")
		} else if !strings.Contains(err.Error(), "output guard failed") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestVerifierFailure(t *testing.T) {
	ctx := context.Background()
	toolLLM := func() core.LLMClient {
		return &capturingLLM{responses: []string{
			`{"thought": "t", "action": {"name": "file.read", "params": {"path": "a"}}}`,
			`{"thought": "t", "answer": "done"}`,
		}}
	}

	t.Run("best effort continues", func(t *testing.T) {
		registry, _ := countingRegistry(t, "file.read", true)
		loop := NewAgentLoop(toolLLM(), NewToolRouter(registry), DefaultLoopConfig())
		loop.SetVerifier(failingVerifier{})

		result, err := loop.Run(ctx, "s", "q")
		if err != nil {
			t.Fatalf("default must continue: %v", err)
		}
		if result.Reason != "completed" {
			t.Errorf("reason = %s", result.Reason)
		}
	})

	t.Run("strict fails the run", func(t *testing.T) {
		registry, _ := countingRegistry(t, "file.read", true)
		loop := NewAgentLoop(toolLLM(), NewToolRouter(registry), DefaultLoopConfig())
		loop.SetVerifier(failingVerifier{})
		loop.SetEffectFailurePolicy(EffectStrict)

		if _, err := loop.Run(ctx, "s", "q"); err == nil {
			t.Fatal("strict must surface the verifier failure")
		} else if !strings.Contains(err.Error(), "verifier failed") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestMemorySaveFailure(t *testing.T) {
	ctx := context.Background()
	toolLLM := func() core.LLMClient {
		return &capturingLLM{responses: []string{
			`{"thought": "t", "action": {"name": "file.read", "params": {"path": "a"}}}`,
			`{"thought": "t", "answer": "done"}`,
			`{"thought": "extract", "answer": "lesson"}`,
		}}
	}
	// Tool steps are what pass the memory write gate, so this run reaches the save.
	newLoop := func(mem core.MemoryStore, strict bool) *AgentLoop {
		registry, _ := countingRegistry(t, "file.read", true)
		loop := NewAgentLoop(toolLLM(), NewToolRouter(registry), DefaultLoopConfig())
		loop.SetMemory(mem)
		if strict {
			loop.SetEffectFailurePolicy(EffectStrict)
		}
		return loop
	}

	t.Run("best effort continues", func(t *testing.T) {
		mem := &failingMemory{}
		result, err := newLoop(mem, false).Run(ctx, "s", "q")
		if err != nil {
			t.Fatalf("default must continue: %v", err)
		}
		if result.Reason != "completed" {
			t.Errorf("reason = %s", result.Reason)
		}
		if mem.saves == 0 {
			t.Error("the save should have been attempted")
		}
	})

	t.Run("strict refuses to report success", func(t *testing.T) {
		mem := &failingMemory{}
		if _, err := newLoop(mem, true).Run(ctx, "s", "q"); err == nil {
			t.Fatal("strict must surface the memory failure")
		} else if !strings.Contains(err.Error(), "save long-term memory") {
			t.Errorf("error = %v", err)
		}
	})
}
