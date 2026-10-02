package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/structured"
)

const (
	// DefaultMaxSteps is the maximum number of Think→Act→Observe cycles.
	DefaultMaxSteps = 20
)

// LoopConfig holds configuration for the ReAct loop.
type LoopConfig struct {
	MaxSteps         int
	MaxContextTokens int
	SystemPrompt     string
}

// DefaultLoopConfig returns config with sensible defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxSteps:         DefaultMaxSteps,
		MaxContextTokens: DefaultMaxContextTokens,
	}
}

// AgentLoop implements the ReAct (Reasoning + Acting) execution loop.
//
// The loop works as follows:
//  1. Send conversation history to LLM with structured output constraint
//  2. Parse LLM response into AgentResponse (Thought + Action or Answer)
//  3. If Answer → return final result
//  4. If Action → call the tool via ToolRouter → append result as observation
//  5. Repeat until Answer or maxSteps exceeded
//
// This is the heart of the Agent system.
type AgentLoop struct {
	llm    core.LLMClient
	router *ToolRouter
	ctxMgr *ContextManager
	config LoopConfig

	// Optional enhancement modules (injected from Agent).
	inputGuard  core.InputGuard
	outputGuard core.OutputGuard
	budget      core.BudgetChecker
	checkpoint  core.CheckpointStore
	memory      core.MemoryStore
	verifier    core.Verifier

	// checkpointPolicy decides whether a failed checkpoint write is fatal.
	checkpointPolicy CheckpointFailurePolicy

	// effectPolicy decides what a failed budget/guard/verifier/memory write means.
	effectPolicy EffectFailurePolicy

	// memoryJudge decides whether a finished run is worth remembering; nil
	// means the default gate (see SetMemoryWriteJudge).
	memoryJudge MemoryWriteJudge
}

// NewAgentLoop creates a new ReAct loop.
func NewAgentLoop(llm core.LLMClient, router *ToolRouter, config LoopConfig) *AgentLoop {
	return &AgentLoop{
		llm:    llm,
		router: router,
		ctxMgr: NewContextManager(config.MaxContextTokens, llm),
		config: config,
	}
}

// SetInputGuard enables M6 input checking.
func (l *AgentLoop) SetInputGuard(g core.InputGuard) { l.inputGuard = g }

// SetOutputGuard enables M6 output filtering.
func (l *AgentLoop) SetOutputGuard(g core.OutputGuard) { l.outputGuard = g }

// SetBudget enables M6 budget enforcement.
func (l *AgentLoop) SetBudget(b core.BudgetChecker) { l.budget = b }

// SetCheckpoint enables M12 state persistence.
func (l *AgentLoop) SetCheckpoint(c core.CheckpointStore) { l.checkpoint = c }

// SetCheckpointFailurePolicy decides what happens when a checkpoint write fails.
// The zero value keeps the best-effort behaviour.
func (l *AgentLoop) SetCheckpointFailurePolicy(p CheckpointFailurePolicy) {
	l.checkpointPolicy = p
}

// SetEffectFailurePolicy decides what a failed budget/guard/verifier/memory
// write does. The zero value keeps the best-effort behaviour.
func (l *AgentLoop) SetEffectFailurePolicy(p EffectFailurePolicy) {
	l.effectPolicy = p
}

// SetMemory enables M5 memory.
func (l *AgentLoop) SetMemory(m core.MemoryStore) { l.memory = m }

// MemoryWriteJudge decides whether a finished run is worth remembering. It is
// called before the lesson-extraction step, so a "no" also saves the LLM call
// that extraction would have cost.
type MemoryWriteJudge func(ctx context.Context, sessionID string, result *RunResult) bool

// SetMemoryWriteJudge overrides the default gate. The default (see
// defaultMemoryWriteJudge) keeps runs that actually did something and skips
// one-shot answers, which have no experience to store.
func (l *AgentLoop) SetMemoryWriteJudge(j MemoryWriteJudge) { l.memoryJudge = j }

// SetVerifier enables D5 self-verification loop.
func (l *AgentLoop) SetVerifier(v core.Verifier) { l.verifier = v }

// StepRecord captures one iteration of the ReAct loop for observability.
type StepRecord struct {
	Step    int
	Thought string
	Action  *structured.ToolCallRequest // nil if terminal
	Result  *core.ToolResult            // nil if terminal
	Answer  string                      // non-empty if terminal
}

// RunResult is the output of a complete agent run.
type RunResult struct {
	Answer string
	Steps  []StepRecord
	// Reason is one of: "completed", "max_steps", "budget_exceeded",
	// "input_blocked", "truncated", "error".
	Reason string
}

// CheckpointFailurePolicy decides what a failed checkpoint write means.
type CheckpointFailurePolicy string

const (
	// CheckpointBestEffort logs the failure and lets the run continue. A
	// checkpoint supports recovery, it is not the task itself, so this is the
	// default.
	CheckpointBestEffort CheckpointFailurePolicy = "best_effort"
	// CheckpointStrict fails the run instead. A run that cannot be recovered
	// should not report success, which is what a caller depending on resume needs.
	CheckpointStrict CheckpointFailurePolicy = "strict"
)

// EffectFailurePolicy decides what a failed safety effect means. Four effects
// used to log and carry on: budget accounting, the output guard, the verifier
// and long-term memory writes. Continuing is right for enhancements, but it
// makes a control that stopped working indistinguishable from one that is
// holding, so a caller who depends on the control can ask to be told.
type EffectFailurePolicy string

const (
	// EffectBestEffort logs the failure and continues: the historical
	// behaviour, kept as the default so existing callers see no change.
	EffectBestEffort EffectFailurePolicy = "best_effort"
	// EffectStrict fails the run instead. A caller that needs the budget
	// enforced, the guard applied, or the memory written should hear when it
	// is not - shipping an unguarded answer as a success is the silent failure
	// this policy exists to prevent.
	//
	// Strict applies to memory writes after a completed run too: the run's
	// result is discarded there, deliberately, because the caller opted into
	// "an unrecorded run is not a successful run".
	EffectStrict EffectFailurePolicy = "strict"
)

// Run executes the full ReAct loop for a user input.
// Returns the final answer and a trace of all steps.
func (l *AgentLoop) Run(ctx context.Context, sessionID string, userInput string) (*RunResult, error) {
	return l.run(ctx, sessionID, userInput, false)
}

// Resume continues a session from its latest checkpoint instead of restarting it.
//
// A session with no checkpoint simply runs from the beginning. When the previous
// attempt stopped while a tool was in flight, the ledger records it, and a tool
// that is not idempotent is never replayed: repeating its side effect would be
// worse than stopping. The run then ends with Reason "unresolved_side_effect".
func (l *AgentLoop) Resume(ctx context.Context, sessionID string) (*RunResult, error) {
	return l.run(ctx, sessionID, "", true)
}

func (l *AgentLoop) run(ctx context.Context, sessionID string, userInput string, resume bool) (*RunResult, error) {
	// --- Input Guard (M6, optional) ---
	// Only a fresh run has new input to check.
	if !resume && l.inputGuard != nil {
		if err := l.inputGuard.Check(ctx, userInput); err != nil {
			return &RunResult{Reason: "input_blocked", Answer: err.Error()}, nil
		}
	}

	messages := []core.Message{
		{Role: "system", Content: l.buildSystemPrompt()},
		{Role: "user", Content: userInput},
	}

	startStep := 0
	var ledger []core.ToolCallRecord

	if resume {
		cp, err := l.loadCheckpoint(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if cp != nil {
			// A finished run has nothing left to do; re-asking the model could
			// only repeat work that was already delivered.
			if cp.Completed {
				return &RunResult{Answer: cp.Answer, Reason: "completed"}, nil
			}

			messages = cp.Messages
			startStep = cp.StepIndex + 1
			ledger = cp.ToolCalls

			if pending, ok := cp.UnresolvedToolCall(); ok {
				// This step was recorded before its tool ran and never recorded a
				// result, so it has to be redone from its own start rather than
				// from the step after it.
				startStep = pending.StepIndex
				if !pending.Idempotent {
					return &RunResult{
						Reason: "unresolved_side_effect",
						Answer: fmt.Sprintf(
							"Stopped instead of resuming: tool %q was in flight when the previous attempt "+
								"ended and is not idempotent, so replaying it could repeat its side effect.",
							pending.Tool),
					}, nil
				}
				// Replaying an idempotent tool is harmless, so drop the dangling
				// record and let the loop redo the step.
				ledger = ledger[:len(ledger)-1]
			}
		}
	}

	// --- Long-term memory recall (M5, optional) ---
	// A fresh run brings a fresh prompt, so this is where the agent "remembers
	// what it learned before". Knowledge retrieval stays agentic (the tool); a
	// run's own past experience is recalled automatically because it is local,
	// bounded (top 3) and cheap. Resumed runs already carry their history.
	if !resume && l.memory != nil && userInput != "" {
		l.recallInto(ctx, &messages, userInput)
	}

	var steps []StepRecord

	for step := startStep; step < l.config.MaxSteps; step++ {
		// --- Budget Check (M6, optional) ---
		if l.budget != nil {
			if err := l.budget.Check(ctx, sessionID); err != nil {
				return &RunResult{
					Answer: "Budget exceeded. Stopping.",
					Steps:  steps,
					Reason: "budget_exceeded",
				}, nil
			}
		}

		// --- Context Window Management (M2) ---
		compacted, err := l.ctxMgr.CompactIfNeeded(ctx, messages)
		if err != nil {
			log.Printf("[harness] context compaction failed: %v", err)
			// Continue with uncompacted messages.
		} else {
			messages = compacted
		}

		// --- Call LLM ---
		chatResult, err := l.llm.ChatWithUsage(ctx, messages)
		if err != nil {
			// Keep the steps accumulated so far. Returning nil here discarded
			// the entire trace and made failures impossible to diagnose.
			return &RunResult{Steps: steps, Reason: "error"},
				fmt.Errorf("step %d: LLM call failed: %w", step, err)
		}

		// --- Budget accounting (M6, optional) ---
		// Record must be called, otherwise usage stays at zero and the budget
		// check above can never trip - which is exactly why a strict caller is
		// told when it did not happen instead of getting a budget that quietly
		// stopped enforcing.
		if l.budget != nil && chatResult.Usage.TotalTokens > 0 {
			if err := l.budget.Record(ctx, sessionID, int64(chatResult.Usage.TotalTokens)); err != nil {
				if l.effectPolicy == EffectStrict {
					return &RunResult{Steps: steps, Reason: "error"},
						fmt.Errorf("step %d: budget record failed: %w", step, err)
				}
				log.Printf("[harness] budget record failed: %v", err)
			}
		}

		// A response cut short by the token limit is incomplete. Treating it as
		// a complete answer silently produced wrong results.
		if chatResult.FinishReason == "length" {
			return &RunResult{Steps: steps, Reason: "truncated"},
				fmt.Errorf("step %d: LLM response truncated (finish_reason=length, %d completion tokens); "+
					"raise MaxTokens or lower the compaction threshold",
					step, chatResult.Usage.CompletionTokens)
		}

		raw := chatResult.Content

		// --- Parse Structured Output (M8) with retry ---
		agentResp, err := structured.ParseWithRetry(raw, func(feedback string) (string, error) {
			retryMsgs := append(messages, core.Message{Role: "assistant", Content: raw})
			retryMsgs = append(retryMsgs, core.Message{Role: "user", Content: feedback})
			return l.llm.Chat(ctx, retryMsgs)
		})
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to parse agent response: %w", step, err)
		}

		// --- Terminal: Agent has a final answer ---
		if agentResp.IsTerminal() {
			answer := agentResp.Answer

			// Output Guard (M6, optional).
			if l.outputGuard != nil {
				filtered, guardErr := l.outputGuard.Check(ctx, answer)
				if guardErr != nil {
					if l.effectPolicy == EffectStrict {
						return nil, fmt.Errorf("output guard failed: %w", guardErr)
					}
					log.Printf("[harness] output guard error: %v", guardErr)
				} else {
					answer = filtered
				}
			}

			steps = append(steps, StepRecord{
				Step:    step,
				Thought: agentResp.Thought,
				Answer:  answer,
			})

			result := &RunResult{
				Answer: answer,
				Steps:  steps,
				Reason: "completed",
			}

			// Record completion so a later Resume returns this answer instead of
			// running the session again.
			if err := l.saveCompletedCheckpoint(ctx, sessionID, step, messages, ledger, answer); err != nil {
				return nil, err
			}

			// Save to long-term memory (M5, optional).
			if err := l.saveMemory(ctx, sessionID, userInput, result); err != nil {
				if l.effectPolicy == EffectStrict {
					return nil, err
				}
				log.Printf("[harness] save memory failed: %v", err)
			}

			return result, nil
		}

		// --- Non-terminal: Agent wants to call a tool ---
		if agentResp.IsToolCall() {
			toolName := agentResp.Action.Name
			ledger = append(ledger, core.ToolCallRecord{
				ID:         fmt.Sprintf("%s-step-%d-%s", sessionID, step, toolName),
				StepIndex:  step,
				Tool:       toolName,
				Idempotent: l.toolIsIdempotent(toolName),
				Status:     core.ToolCallStarted,
				StartedAt:  time.Now().UTC(),
			})
			// Record the intent before invoking the tool. A crash between here and
			// the completion record below is exactly the case a resume has to be
			// able to see, and it can only see it if it was written first.
			if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
				return nil, err
			}

			toolResult := l.router.Call(ctx, toolName, agentResp.Action.Params)

			ledger[len(ledger)-1].Status = core.ToolCallCompleted
			ledger[len(ledger)-1].Result = toolResult.Output
			ledger[len(ledger)-1].Error = toolResult.Error
			ledger[len(ledger)-1].CompletedAt = time.Now().UTC()

			steps = append(steps, StepRecord{
				Step:    step,
				Thought: agentResp.Thought,
				Action:  agentResp.Action,
				Result:  toolResult,
			})

			// Echo the assistant turn back losslessly. The previous hand-built
			// string kept only the tool name, so the model could not see which
			// arguments it had just supplied.
			messages = append(messages, core.Message{
				Role:    "assistant",
				Content: marshalAssistantTurn(agentResp),
			})

			// Format tool result as observation.
			observation := formatObservation(agentResp.Action.Name, toolResult)
			messages = append(messages, core.Message{
				Role:    "user",
				Content: observation,
			})

			// --- Reflexion (AE-4): on tool failure, ask LLM to reflect before retrying ---
			if toolResult.Error != "" {
				reflection := l.reflect(ctx, messages, agentResp.Action, toolResult.Error)
				if reflection != "" {
					messages = append(messages, core.Message{
						Role:    "user",
						Content: fmt.Sprintf("[Reflection]: %s", reflection),
					})
				}
			}

			// --- Verify (D5, optional) ---
			if l.verifier != nil {
				verifyAction := core.ToolCall{
					Name:   agentResp.Action.Name,
					Params: fmt.Sprintf("%v", agentResp.Action.Params),
				}
				ok, feedback, verifyErr := l.verifier.Verify(ctx, verifyAction, toolResult)
				if verifyErr != nil {
					if l.effectPolicy == EffectStrict {
						return nil, fmt.Errorf("verifier failed: %w", verifyErr)
					}
					log.Printf("[harness] verifier error: %v", verifyErr)
				} else if !ok {
					messages = append(messages, core.Message{
						Role:    "user",
						Content: fmt.Sprintf("[Verification failed]: %s\nPlease try a different approach.", feedback),
					})
				}
			}

			// Persist the finished step. This is the checkpoint a resume loads.
			if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
				return nil, err
			}

			continue
		}

		// Should not reach here — Validate() ensures one of the two paths.
		return nil, fmt.Errorf("step %d: agent response is neither terminal nor tool call", step)
	}

	// --- Max steps exceeded ---
	return &RunResult{
		Answer: "I've reached the maximum number of steps. Here's what I found so far.",
		Steps:  steps,
		Reason: "max_steps",
	}, nil
}

// loadCheckpoint returns the newest checkpoint for a session, or nil when the
// session has none. An empty session is a normal first call, not a failure.
func (l *AgentLoop) loadCheckpoint(ctx context.Context, sessionID string) (*core.Checkpoint, error) {
	if l.checkpoint == nil {
		return nil, fmt.Errorf("resume requires a checkpoint store")
	}
	cp, err := l.checkpoint.Latest(ctx, sessionID)
	if err != nil {
		if errors.Is(err, core.ErrNoCheckpoint) {
			return nil, nil
		}
		return nil, fmt.Errorf("load checkpoint for session %s: %w", sessionID, err)
	}
	return cp, nil
}

// persistCheckpoint writes a checkpoint, applying the failure policy. It is the
// single place that decides whether a failed write is fatal.
func (l *AgentLoop) persistCheckpoint(ctx context.Context, cp *core.Checkpoint) error {
	if l.checkpoint == nil {
		return nil
	}
	if err := l.checkpoint.Save(ctx, cp); err != nil {
		if l.checkpointPolicy == CheckpointStrict {
			return fmt.Errorf("checkpoint save failed for session %s step %d: %w",
				cp.SessionID, cp.StepIndex, err)
		}
		log.Printf("[harness] checkpoint save failed, step %d of session %s cannot be resumed: %v",
			cp.StepIndex, cp.SessionID, err)
	}
	return nil
}

// saveCheckpoint persists one step together with the side-effect ledger.
func (l *AgentLoop) saveCheckpoint(
	ctx context.Context,
	sessionID string,
	step int,
	messages []core.Message,
	ledger []core.ToolCallRecord,
) error {
	return l.persistCheckpoint(ctx, &core.Checkpoint{
		ID:        fmt.Sprintf("%s-step-%d", sessionID, step),
		SessionID: sessionID,
		StepIndex: step,
		Messages:  messages,
		ToolCalls: ledger,
		CreatedAt: time.Now().UTC(),
	})
}

// saveCompletedCheckpoint records the final answer so a later resume can return
// it instead of running the session a second time.
func (l *AgentLoop) saveCompletedCheckpoint(
	ctx context.Context,
	sessionID string,
	step int,
	messages []core.Message,
	ledger []core.ToolCallRecord,
	answer string,
) error {
	return l.persistCheckpoint(ctx, &core.Checkpoint{
		ID:        fmt.Sprintf("%s-step-%d", sessionID, step),
		SessionID: sessionID,
		StepIndex: step,
		Messages:  messages,
		ToolCalls: ledger,
		Completed: true,
		Answer:    answer,
		CreatedAt: time.Now().UTC(),
	})
}

// toolIsIdempotent reports whether replaying a tool is harmless. An unknown tool
// reports false, so a tool nobody declared is never replayed.
func (l *AgentLoop) toolIsIdempotent(name string) bool {
	if l.router == nil {
		return false
	}
	def := l.router.registry.GetTool(name)
	return def != nil && def.Idempotent
}

// buildSystemPrompt creates the system prompt including tool descriptions
// and output format instructions.
func (l *AgentLoop) buildSystemPrompt() string {
	base := l.config.SystemPrompt
	if base == "" {
		base = `You are an AI agent that completes tasks with tools.
Think step by step, and prefer acting over describing.`
	}

	toolList := l.router.ListTools()

	schema := structured.FormatForLLM(structured.GenerateSchema(structured.AgentResponse{}))

	return fmt.Sprintf(`%s

%s

You must respond in JSON format with this schema:
%s

Rules:
1. Always include "thought" — explain your reasoning
2. To use a tool, set "action" with "name" and "params"
3. To give a final answer, set "answer" (no action)
4. Never set both "action" and "answer"
5. If a tool returns an error, try an alternative approach or explain the issue`, base, toolList, schema)
}

// marshalAssistantTurn renders an assistant tool-calling turn back into the
// conversation. Re-marshalling the parsed response preserves the tool
// parameters; the fallback omits them but keeps the turn well-formed.
func marshalAssistantTurn(resp *structured.AgentResponse) string {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"thought": %q, "action": {"name": %q}}`, resp.Thought, resp.Action.Name)
	}
	return string(encoded)
}

// formatObservation formats a tool result as an observation message for the LLM.
func formatObservation(toolName string, result *core.ToolResult) string {
	if result.Error != "" {
		return fmt.Sprintf("[Tool %q returned error]: %s", toolName, result.Error)
	}
	return fmt.Sprintf("[Tool %q result]: %s", toolName, result.Output)
}

// saveMemory extracts a lesson from the run and saves to long-term memory.
// Uses LLM to distill the experience if available, otherwise falls back to a simple summary.
// saveMemory runs the write-side gate and persists the lesson. The failure is
// returned rather than logged: whether it is fatal is the run's policy to
// decide, not the write's.
func (l *AgentLoop) saveMemory(ctx context.Context, sessionID, userInput string, result *RunResult) error {
	if l.memory == nil {
		return nil
	}

	// The gate runs before extraction: a run judged not worth remembering also
	// skips the lesson-extraction LLM call that used to be paid unconditionally.
	judge := l.memoryJudge
	if judge == nil {
		judge = defaultMemoryWriteJudge
	}
	if !judge(ctx, sessionID, result) {
		return nil
	}

	lesson := l.extractLesson(ctx, userInput, result)

	entry := core.MemoryEntry{
		ID:       fmt.Sprintf("%s_%d", sessionID, time.Now().UnixNano()),
		Content:  lesson,
		Category: "experience",
	}

	if err := l.memory.SaveLongTerm(ctx, entry); err != nil {
		return fmt.Errorf("save long-term memory: %w", err)
	}
	return nil
}

// defaultMemoryWriteJudge remembers a completed run only when it actually used
// tools. A terminal answer also produces a StepRecord, so counting steps is not
// enough - the gate looks for an action. A run that answered in the first turn
// produced an answer, not experience; storing it would add noise and cost an
// extraction call for nothing.
func defaultMemoryWriteJudge(_ context.Context, _ string, result *RunResult) bool {
	if result == nil || result.Reason != "completed" {
		return false
	}
	for _, step := range result.Steps {
		if step.Action != nil {
			return true
		}
	}
	return false
}

// recallInto inserts the top matching long-term memories as a system block
// directly after the prompt. Recall failures are logged, never fatal: memory is
// an enhancement, and a broken index must not stop a run.
func (l *AgentLoop) recallInto(ctx context.Context, messages *[]core.Message, userInput string) {
	entries, err := l.memory.SearchLongTerm(ctx, userInput, 3)
	if err != nil {
		log.Printf("[harness] memory recall failed: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	var block strings.Builder
	block.WriteString("Relevant experience from previous runs (use only where it applies):\n")
	for _, entry := range entries {
		fmt.Fprintf(&block, "- [%s] %s\n", entry.Category, truncate(entry.Content, 300))
	}

	msgs := *messages
	rest := make([]core.Message, 0, len(msgs))
	rest = append(rest, msgs[1:]...)
	*messages = append([]core.Message{msgs[0], {Role: "system", Content: block.String()}}, rest...)
}

// extractLesson uses the LLM to distill a reusable lesson from the completed run.
// Falls back to a simple summary if LLM is unavailable or fails.
func (l *AgentLoop) extractLesson(ctx context.Context, userInput string, result *RunResult) string {
	fallback := fmt.Sprintf("Task: %s | Steps: %d | Result: %s",
		truncate(userInput, 100),
		len(result.Steps),
		truncate(result.Answer, 200),
	)

	if l.llm == nil {
		return fallback
	}

	// Build step log for the LLM.
	var stepLog string
	for i, step := range result.Steps {
		if i >= 5 {
			stepLog += fmt.Sprintf("... and %d more steps\n", len(result.Steps)-5)
			break
		}
		detail := step.Thought
		if step.Action != nil {
			detail = fmt.Sprintf("called %s", step.Action.Name)
		}
		if step.Answer != "" {
			detail = fmt.Sprintf("answered: %s", truncate(step.Answer, 100))
		}
		stepLog += fmt.Sprintf("Step %d: %s\n", i+1, truncate(detail, 150))
	}

	extractMsgs := []core.Message{
		{
			Role: "system",
			Content: "You are reviewing a completed agent task. Extract a concise lesson (1-2 sentences) " +
				"that would help with similar future tasks. Focus on: what worked, what failed, " +
				"any tricks or prerequisites discovered. Be specific and actionable.",
		},
		{
			Role: "user",
			Content: fmt.Sprintf("Task: %s\n\nSteps:\n%s\nFinal answer: %s",
				truncate(userInput, 200), stepLog, truncate(result.Answer, 300)),
		},
	}

	lesson, err := l.llm.Chat(ctx, extractMsgs)
	if err != nil {
		log.Printf("[harness] extractLesson LLM call failed, using fallback: %v", err)
		return fallback
	}
	return lesson
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// reflect asks the LLM to analyze a tool failure and suggest a revised approach.
// Returns the reflection text, or empty string if LLM is unavailable or fails.
func (l *AgentLoop) reflect(ctx context.Context, messages []core.Message,
	action *structured.ToolCallRequest, toolError string) string {

	if l.llm == nil {
		return ""
	}

	reflectPrompt := fmt.Sprintf(
		`The tool call "%s" failed with error: %s

Reflect on why this failed. Consider:
1. Were the parameters correct?
2. Is there a prerequisite step that was missed?
3. Should a different tool be used instead?
4. What specific changes would fix this?

Provide a brief analysis and revised plan (2-3 sentences).`,
		action.Name, toolError)

	reflectMsgs := append(messages, core.Message{
		Role:    "user",
		Content: reflectPrompt,
	})

	reflection, err := l.llm.Chat(ctx, reflectMsgs)
	if err != nil {
		log.Printf("[harness] reflect LLM call failed: %v", err)
		return ""
	}
	return reflection
}
