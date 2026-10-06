package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/castwell/forge/internal/agent/core"
	"github.com/castwell/forge/internal/agent/structured"
)

// The native (provider function-calling) tool path — N2.
//
// The batch shares every invariant with the prompt path, deliberately: the
// duplicate guard, the streak accounting, the G3 output screening, Reflexion,
// verification, and the journal-before-checkpoint ordering. What changes is
// only where the calls come from (message.tool_calls instead of parsed JSON)
// and how history is echoed: an assistant message carrying its requests, and
// a tool-role answer per call — the protocol order the providers expect.

// runNativeToolBatch executes one step's worth of native tool calls in
// sequence. Execution within a step is sequential on purpose: side-effecting
// handlers are not thread-safe, and a deterministic order makes the ledger
// readable — the structure no longer forbids batching, the runtime chooses
// not to interleave (see D-29 boundaries).
func (l *AgentLoop) runNativeToolBatch(
	ctx context.Context,
	sessionID string,
	step int,
	messages *[]core.Message,
	steps *[]StepRecord,
	ledger *[]core.ToolCallRecord,
	journalBase *int,
	chatResult core.ChatResult,
) error {
	thought := chatResult.Content
	calls := chatResult.ToolCalls

	// Protocol order: the assistant's requests precede their answers.
	*messages = append(*messages, core.Message{
		Role:      "assistant",
		Content:   thought,
		ToolCalls: calls,
	})

	for i, call := range calls {
		// Context tools are loop meta-operations (N2c): they act on the
		// loop's own window, so they get no ledger entry, no duplicate guard
		// and no streak. The assistant message carrying the whole batch was
		// appended above — exactly where a compaction needs to find it.
		if isContextTool(call.Name) {
			toolResult := l.runContextTool(ctx, call.Name, call.Arguments, messages, sessionID, step)
			if toolResult.Error == "" && call.Name == "context.compact" {
				// Wholesale replacement: restart the journal delta at the new
				// snapshot; the observation appended next is the only thing
				// this step should hand a rebuild.
				*journalBase = len(*messages)
			}
			*messages = append(*messages, core.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    formatObservation(call.Name, toolResult),
			})
			*steps = append(*steps, StepRecord{
				Step:    step,
				Thought: thought,
				Action:  &structured.ToolCallRequest{Name: call.Name, Params: call.Arguments},
				Result:  toolResult,
			})
			continue
		}

		id := fmt.Sprintf("%s-step-%d-%s", sessionID, step, call.Name)
		if i > 0 {
			id += fmt.Sprintf("-%d", i)
		}
		*ledger = append(*ledger, core.ToolCallRecord{
			ID:         id,
			StepIndex:  step,
			Tool:       call.Name,
			Params:     call.Arguments,
			Idempotent: l.toolIsIdempotent(call.Name),
			Status:     core.ToolCallStarted,
			StartedAt:  time.Now().UTC(),
		})
		rec := &(*ledger)[len(*ledger)-1]

		// Intent journal first, then checkpoint: the first call of the batch
		// carries the turn (a rebuild reconstructs the assistant message from
		// it); every call carries its provider-native id so a partial-step
		// rebuild can answer the right request.
		turn := ""
		if i == 0 {
			turn = marshalNativeTurn(thought, calls)
		}
		if jerr := l.journalAppend(ctx, RunEvent{
			RunID: sessionID,
			Type:  EventToolStarted,
			Step:  step,
			Tool:  call.Name,
			TS:    rec.StartedAt,
			Data: map[string]any{
				"id":         rec.ID,
				"idempotent": rec.Idempotent,
				"params":     call.Arguments,
				"native":     true,
				"native_id":  call.ID,
				"turn":       turn,
			},
		}); jerr != nil {
			return jerr
		}
		if err := l.saveCheckpoint(ctx, sessionID, step, *messages, *ledger); err != nil {
			return err
		}

		// The duplicate guard and streak accounting are the SAME code as the
		// prompt path (no_progress.go methods), so the idempotency contract
		// cannot drift between the two paths.
		fingerprint := toolFingerprint(call.Name, call.Arguments)
		l.noteProgress(fingerprint)

		var toolResult *core.ToolResult
		if !rec.Idempotent && l.nonIdempotentDone[fingerprint] {
			toolResult = &core.ToolResult{
				Error: fmt.Sprintf(duplicateRefusalFormat, call.Name, compactParams(call.Arguments)),
			}
		} else {
			toolResult = l.router.Call(ctx, call.Name, call.Arguments)
			if !rec.Idempotent {
				l.nonIdempotentDone[fingerprint] = true
			}
		}

		rec.Status = core.ToolCallCompleted
		rec.Result = toolResult.Output
		rec.Error = toolResult.Error
		rec.CompletedAt = time.Now().UTC()

		if jerr := l.journalAppend(ctx, RunEvent{
			RunID: sessionID,
			Type:  EventToolCompleted,
			Step:  step,
			Tool:  call.Name,
			TS:    rec.CompletedAt,
			Data: map[string]any{
				"id":        rec.ID,
				"result":    toolResult.Output,
				"error":     toolResult.Error,
				"native_id": call.ID,
			},
		}); jerr != nil {
			return jerr
		}

		*steps = append(*steps, StepRecord{
			Step:    step,
			Thought: thought,
			Action:  &structured.ToolCallRequest{Name: call.Name, Params: call.Arguments},
			Result:  toolResult,
		})

		// G3 invariant: tool output is untrusted and is screened before it
		// becomes a message. The ledger and journal above keep the raw truth.
		screened := toolResult
		if l.toolOutputGuard != nil && toolResult.Output != "" {
			if gerr := l.toolOutputGuard.Check(ctx, toolResult.Output); gerr != nil {
				if l.effectPolicy == EffectStrict {
					return fmt.Errorf("step %d: tool output guard blocked %q output: %w", step, call.Name, gerr)
				}
				log.Printf("[harness] tool output guard blocked %q output: %v", call.Name, gerr)
				replace := *toolResult
				replace.Output = fmt.Sprintf("[tool output blocked by guard: %v]", gerr)
				screened = &replace
			}
		}

		*messages = append(*messages, core.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    formatObservation(call.Name, screened),
		})

		// Reflexion on failure — same gate as the prompt path.
		if toolResult.Error != "" {
			action := &structured.ToolCallRequest{Name: call.Name, Params: call.Arguments}
			if reflection := l.reflect(ctx, *messages, action, toolResult.Error); reflection != "" {
				*messages = append(*messages, core.Message{
					Role:    "user",
					Content: fmt.Sprintf("[Reflection]: %s", reflection),
				})
			}
		}

		// Verify (D5) — same policy handling as the prompt path.
		if l.verifier != nil {
			verifyAction := core.ToolCall{
				Name:   call.Name,
				Params: fmt.Sprintf("%v", call.Arguments),
			}
			ok, feedback, verifyErr := l.verifier.Verify(ctx, verifyAction, toolResult)
			if verifyErr != nil {
				if l.effectPolicy == EffectStrict {
					return fmt.Errorf("verifier failed: %w", verifyErr)
				}
				log.Printf("[harness] verifier error: %v", verifyErr)
			} else if !ok {
				*messages = append(*messages, core.Message{
					Role:    "user",
					Content: fmt.Sprintf("[Verification failed]: %s\nPlease try a different approach.", feedback),
				})
			}
		}
	}

	// Journal the finished step's delta, then checkpoint — same order and
	// same meaning as the prompt path: a rebuild needs this delta, a resume
	// loads this checkpoint.
	if jerr := l.journalAppend(ctx, RunEvent{
		RunID: sessionID,
		Type:  EventStepCompleted,
		Step:  step,
		TS:    time.Now().UTC(),
		Data:  map[string]any{"turns": (*messages)[*journalBase:]},
	}); jerr != nil {
		return jerr
	}
	*journalBase = len(*messages)
	return l.saveCheckpoint(ctx, sessionID, step, *messages, *ledger)
}

// marshalNativeTurn records the assistant's batch for the journal: a rebuild
// of a crashed run reconstructs the assistant message (with its requests)
// from this instead of from the plain text the prompt path uses.
func marshalNativeTurn(thought string, calls []core.NativeToolCall) string {
	payload := map[string]any{
		"thought":    thought,
		"native":     true,
		"tool_calls": calls,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return thought
	}
	return string(encoded)
}

// decodeNativeTurn reads a journaled turn back. ok is false when the turn is
// a plain (prompt-path) assistant message.
func decodeNativeTurn(turn string) (string, []core.NativeToolCall, bool) {
	var payload struct {
		Thought   string                `json:"thought"`
		Native    bool                  `json:"native"`
		ToolCalls []core.NativeToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(turn), &payload); err != nil || !payload.Native {
		return "", nil, false
	}
	return payload.Thought, payload.ToolCalls, true
}
