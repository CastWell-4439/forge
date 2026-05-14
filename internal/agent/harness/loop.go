package harness

import (
	"context"
	"fmt"
	"log"
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
	llm     core.LLMClient
	router  *ToolRouter
	ctxMgr  *ContextManager
	config  LoopConfig

	// Optional enhancement modules (injected from Agent).
	inputGuard  core.InputGuard
	outputGuard core.OutputGuard
	budget      core.BudgetChecker
	checkpoint  core.CheckpointStore
	memory      core.MemoryStore
	verifier    core.Verifier
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

// SetMemory enables M5 memory.
func (l *AgentLoop) SetMemory(m core.MemoryStore) { l.memory = m }

// SetVerifier enables D5 self-verification loop.
func (l *AgentLoop) SetVerifier(v core.Verifier) { l.verifier = v }

// StepRecord captures one iteration of the ReAct loop for observability.
type StepRecord struct {
	Step     int
	Thought  string
	Action   *structured.ToolCallRequest // nil if terminal
	Result   *core.ToolResult            // nil if terminal
	Answer   string                      // non-empty if terminal
}

// RunResult is the output of a complete agent run.
type RunResult struct {
	Answer   string
	Steps    []StepRecord
	Reason   string // "completed", "max_steps", "budget_exceeded", "error"
}

// Run executes the full ReAct loop for a user input.
// Returns the final answer and a trace of all steps.
func (l *AgentLoop) Run(ctx context.Context, sessionID string, userInput string) (*RunResult, error) {
	// --- Input Guard (M6, optional) ---
	if l.inputGuard != nil {
		if err := l.inputGuard.Check(ctx, userInput); err != nil {
			return &RunResult{Reason: "input_blocked", Answer: err.Error()}, nil
		}
	}

	// Build the system prompt with tool descriptions.
	systemPrompt := l.buildSystemPrompt()

	messages := []core.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userInput},
	}

	var steps []StepRecord

	for step := 0; step < l.config.MaxSteps; step++ {
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
		raw, err := l.llm.Chat(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("step %d: LLM call failed: %w", step, err)
		}

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

			// Save to long-term memory (M5, optional).
			l.saveMemory(ctx, sessionID, userInput, result)

			return result, nil
		}

		// --- Non-terminal: Agent wants to call a tool ---
		if agentResp.IsToolCall() {
			// Invoke the tool via ToolRouter.
			toolResult := l.router.Call(ctx, agentResp.Action.Name, agentResp.Action.Params)

			steps = append(steps, StepRecord{
				Step:    step,
				Thought: agentResp.Thought,
				Action:  agentResp.Action,
				Result:  toolResult,
			})

			// Append assistant message (the agent's response) and tool result
			// as observation for the next iteration.
			assistantContent := fmt.Sprintf(
				`{"thought": %q, "action": {"name": %q}}`,
				agentResp.Thought, agentResp.Action.Name,
			)
			messages = append(messages, core.Message{
				Role:    "assistant",
				Content: assistantContent,
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
					log.Printf("[harness] verifier error: %v", verifyErr)
				} else if !ok {
					messages = append(messages, core.Message{
						Role:    "user",
						Content: fmt.Sprintf("[Verification failed]: %s\nPlease try a different approach.", feedback),
					})
				}
			}

			// Save checkpoint (M12, optional).
			if l.checkpoint != nil {
				cp := &core.Checkpoint{
					ID:        fmt.Sprintf("%s-step-%d", sessionID, step),
					SessionID: sessionID,
					StepIndex: step,
					Messages:  messages,
				}
				if err := l.checkpoint.Save(ctx, cp); err != nil {
					log.Printf("[harness] checkpoint save failed: %v", err)
				}
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

// buildSystemPrompt creates the system prompt including tool descriptions
// and output format instructions.
func (l *AgentLoop) buildSystemPrompt() string {
	base := l.config.SystemPrompt
	if base == "" {
		base = `You are an AI agent that helps with video production tasks.
You can use tools to accomplish tasks. Think step by step.`
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

// formatObservation formats a tool result as an observation message for the LLM.
func formatObservation(toolName string, result *core.ToolResult) string {
	if result.Error != "" {
		return fmt.Sprintf("[Tool %q returned error]: %s", toolName, result.Error)
	}
	return fmt.Sprintf("[Tool %q result]: %s", toolName, result.Output)
}

// saveMemory extracts a lesson from the run and saves to long-term memory.
// Uses LLM to distill the experience if available, otherwise falls back to a simple summary.
func (l *AgentLoop) saveMemory(ctx context.Context, sessionID, userInput string, result *RunResult) {
	if l.memory == nil {
		return
	}

	lesson := l.extractLesson(ctx, userInput, result)

	entry := core.MemoryEntry{
		ID:       fmt.Sprintf("%s_%d", sessionID, time.Now().UnixNano()),
		Content:  lesson,
		Category: "experience",
	}

	if err := l.memory.SaveLongTerm(ctx, entry); err != nil {
		log.Printf("[harness] save memory failed: %v", err)
	}
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
