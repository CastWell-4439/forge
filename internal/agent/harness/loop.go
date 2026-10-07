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
	// NoProgressThreshold ends the run with Reason "no_progress" after this
	// many consecutive identical tool calls. 0 means DefaultNoProgressThreshold;
	// a negative value disables the streak stop (the non-idempotent duplicate
	// guard stays on regardless — see no_progress.go).
	NoProgressThreshold int
	// NativeTools asks the model for provider-native function calling
	// (tools/tool_calls with per-parameter JSON Schema) instead of the
	// prompt-encoded JSON convention. It is a request, not a promise: an
	// endpoint that refuses the tools field downgrades this run to the prompt
	// path with one warning (see ChatWithTools). Clients that do not implement
	// ToolAwareLLM — every test mock — simply stay on the prompt path.
	NativeTools bool
	// CompactKeepMessages is how many trailing messages survive a compaction
	// un-summarised (static rule, S1). 0 picks defaultCompactKeepMessages.
	CompactKeepMessages int
	// ContextCompactTarget is the fraction of the budget a compaction aims for
	// (S4). 0 picks defaultCompactTarget (0.60). Negative means "drop the whole
	// candidate range" (the model's explicit request).
	ContextCompactTarget float64
	// ContextRemindAt is the first water line as a fraction of the window
	// (0.70 default): crossing it injects ONE relative-value reminder.
	// Negative disables the reminders entirely. ContextUrgentAt is the second
	// line (0.90 default) with stronger wording.
	ContextRemindAt float64
	ContextUrgentAt float64
	// Authority is the ceiling this run may act under (L0..L4). Empty means
	// core.DefaultAuthority (L2: read-only without asking). It is a property of
	// the RUN, not of the workflow: a pipeline author cannot raise it.
	Authority core.Authority
	// TaskEffect is what the workflow declared this task does (read < write <
	// delete). The gate takes the STRONGER of this and the tool's own effect,
	// so a lenient task cannot wave a destructive tool through. Empty means
	// write, matching the tool-side default.
	TaskEffect core.ToolEffect
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
	// toolOutputGuard screens untrusted tool output before it reaches the
	// model. Tool results (web pages, file contents, MCP server replies)
	// arrive as user-role observations, which makes them the classic indirect
	// injection vector: a page saying "ignore all previous instructions" would
	// otherwise be delivered to the model as an instruction. It is an
	// InputGuard rather than an OutputGuard because the direction is
	// "untrusted text going INTO the model", not "our answer going OUT".
	toolOutputGuard core.InputGuard
	budget          core.BudgetChecker
	checkpoint      core.CheckpointStore
	journal         Journal
	memory          core.MemoryStore
	verifier        core.Verifier

	// checkpointPolicy decides whether a failed checkpoint write is fatal.
	checkpointPolicy CheckpointFailurePolicy

	// effectPolicy decides what a failed budget/guard/verifier/memory write means.
	effectPolicy EffectFailurePolicy

	// memoryJudge decides whether a finished run is worth remembering; nil
	// means the default gate (see SetMemoryWriteJudge).
	memoryJudge MemoryWriteJudge

	// lessons is the read-only lessons channel (F3) and lessonFilter the
	// read-side gate that decides which recalled lessons reach the prompt.
	// Both nil = no lesson recall, which is the historical behaviour.
	lessons      core.LessonSource
	lessonFilter LessonFilter

	// Duplicate detection state (see no_progress.go). Rebuilt from the ledger
	// at the start of every run, including resumes.
	nonIdempotentDone map[string]bool
	lastFingerprint   string
	noProgress        int

	// Context-window state (N2c): the archive compaction feeds recall, and
	// one-shot flags per water line so a reminder never repeats within a run.
	archive          *ContextArchive
	remindedWarn     bool
	remindedCritical bool
}

// NewAgentLoop creates a new ReAct loop.
func NewAgentLoop(llm core.LLMClient, router *ToolRouter, config LoopConfig) *AgentLoop {
	ctxMgr := NewContextManager(config.MaxContextTokens, llm)
	// S1/S4 wiring: the static path honours the same knobs as the model's
	// explicit compaction — that shared-ness is what makes them one policy
	// instead of two that drift.
	if config.CompactKeepMessages > 0 {
		ctxMgr.keep = config.CompactKeepMessages
	}
	if config.ContextCompactTarget > 0 {
		ctxMgr.compactTarget = config.ContextCompactTarget
	}
	return &AgentLoop{
		llm:     llm,
		router:  router,
		ctxMgr:  ctxMgr,
		config:  config,
		archive: NewContextArchive(0),
	}
}

// SetInputGuard enables M6 input checking.
func (l *AgentLoop) SetInputGuard(g core.InputGuard) { l.inputGuard = g }

// SetOutputGuard enables M6 output filtering.
func (l *AgentLoop) SetOutputGuard(g core.OutputGuard) { l.outputGuard = g }

// SetToolOutputGuard enables screening of tool output before it is shown to
// the model (nil keeps the historical behaviour: tool output passes through
// untouched). A blocked output is replaced by a placeholder so the run
// continues and the model can try another approach; only EffectStrict turns
// a blocked output into a failed run.
func (l *AgentLoop) SetToolOutputGuard(g core.InputGuard) { l.toolOutputGuard = g }

// SetBudget enables M6 budget enforcement.
func (l *AgentLoop) SetBudget(b core.BudgetChecker) { l.budget = b }

// SetCheckpoint enables M12 state persistence.
func (l *AgentLoop) SetCheckpoint(c core.CheckpointStore) { l.checkpoint = c }

// SetJournal enables the run journal (D-12). A nil journal disables all
// journaling, which keeps existing callers behaviour-identical.
func (l *AgentLoop) SetJournal(j Journal) { l.journal = j }

// journalAppend writes one run event under the checkpoint failure policy: the
// journal is recovery state just like a checkpoint, so a caller that wants
// "an unrecorded run is not a successful run" gets it from the same switch
// (CheckpointStrict) instead of a second configuration knob.
func (l *AgentLoop) journalAppend(ctx context.Context, ev RunEvent) error {
	if l.journal == nil {
		return nil
	}
	if err := l.journal.AppendEvent(ctx, ev); err != nil {
		if l.checkpointPolicy == CheckpointStrict {
			return fmt.Errorf("journal %s for session %s: %w", ev.Type, ev.RunID, err)
		}
		log.Printf("[harness] journal %s for session %s not durable: %v", ev.Type, ev.RunID, err)
	}
	return nil
}

// emitRunEnded records a run that ended without a final answer. These events
// are audit-only — rebuild ignores them — so a write failure here is logged,
// never fatal, even under CheckpointStrict (the run is already failing with a
// real error that the caller must see instead).
func (l *AgentLoop) emitRunEnded(ctx context.Context, sessionID string, step int, reason, detail string) {
	if l.journal == nil {
		return
	}
	err := l.journalAppend(ctx, RunEvent{
		RunID: sessionID,
		Type:  EventRunEnded,
		Step:  step,
		TS:    time.Now().UTC(),
		Data:  map[string]any{"reason": reason, "detail": detail},
	})
	if err != nil {
		log.Printf("[harness] run_ended journal write failed: %v", err)
	}
}

// emitRunStart journals where a logical run begins. A fresh run records its
// initial messages as a snapshot; a resume records the state it resumes from
// (messages plus the step index a rebuild should continue after), so rebuild
// never depends on events written before that point.
func (l *AgentLoop) emitRunStart(ctx context.Context, sessionID string, resumed, hasState bool,
	source, userInput string, messages []core.Message, startStep int) error {

	if l.journal == nil {
		return nil
	}
	if resumed && hasState {
		return l.journalAppend(ctx, RunEvent{
			RunID: sessionID,
			Type:  EventRunResumed,
			TS:    time.Now().UTC(),
			Data: map[string]any{
				"messages":   messages,
				"step_index": startStep - 1,
				"source":     source,
			},
		})
	}
	data := map[string]any{"messages": messages, "input": userInput}
	if resumed {
		data["resumed"] = true
	}
	return l.journalAppend(ctx, RunEvent{RunID: sessionID, Type: EventRunStarted, TS: time.Now().UTC(), Data: data})
}

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

// LessonFilter decides which recalled lessons are worth putting in front of the
// model. It is the read-side counterpart of MemoryWriteJudge: same shape
// (explicit, replaceable, deterministic by default), opposite direction — that
// gate decides what the agent writes about itself, this one decides what
// another plane's lessons may claim a place in the prompt.
//
// sessionID is passed so a filter can drop lessons produced BY this very run:
// feeding an agent its own verdict is how a system starts teaching itself its
// own mistakes.
type LessonFilter func(ctx context.Context, sessionID string, item core.RecallItem) bool

// SetLessonFilter overrides the default lesson filter (see defaultLessonFilter).
func (l *AgentLoop) SetLessonFilter(f LessonFilter) { l.lessonFilter = f }

// SetLessonSource installs the read-only lessons channel. A nil source keeps
// the historical behaviour: no lesson recall at all.
func (l *AgentLoop) SetLessonSource(s core.LessonSource) { l.lessons = s }

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
	// "input_blocked", "truncated", "paused", "no_progress", "error".
	Reason string
	// PauseReason explains a "paused" ending: what the run needs a human to
	// decide. Empty for every other reason. The person releasing the run reads
	// only this, so a pause that cannot say why is refused before it happens
	// (see the agent.pause interception).
	PauseReason string
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
	var cp *core.Checkpoint
	resumeSource := ""

	if resume {
		var err error
		// With a checkpoint store, load it. Without one, resume still works
		// when a journal is configured (the journal can rebuild on its own);
		// with neither, loadCheckpoint keeps reporting the old, still-true
		// error: resume requires some recovery state.
		if l.checkpoint != nil || l.journal == nil {
			cp, err = l.loadCheckpoint(ctx, sessionID)
			if err != nil {
				return nil, err
			}
		}
		if cp != nil {
			resumeSource = "checkpoint"
		} else if l.journal != nil {
			// No usable checkpoint: fall back to the journal (D-12 slow path).
			// Every rebuild failure except "no events at all" refuses the run —
			// a gap means the state cannot be trusted, and running anyway could
			// repeat a side effect that already happened. A journal read failure
			// also refuses: without it there is no way to tell a clean slate from
			// a lost one.
			events, rerr := l.journal.ReadEvents(ctx, sessionID)
			if rerr != nil {
				return nil, fmt.Errorf("read journal for session %s: %w", sessionID, rerr)
			}
			if len(events) > 0 {
				cp, err = RebuildCheckpoint(events)
				if err == nil {
					resumeSource = "journal"
				} else if !errors.Is(err, ErrNoJournalEvents) {
					return nil, fmt.Errorf("resume session %s from journal: %w", sessionID, err)
				}
			}
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

	// Duplicate/no-progress detection starts from whatever the ledger already
	// knows: side effects recorded before a crash still count, so a resumed
	// run keeps refusing calls that already ran.
	l.initProgressTracking(ledger)

	// --- Long-term memory recall (M5, optional) ---
	// A fresh run brings a fresh prompt, so this is where the agent "remembers
	// what it learned before". Knowledge retrieval stays agentic (the tool); a
	// run's own past experience is recalled automatically because it is local,
	// bounded (top 3) and cheap. Resumed runs already carry their history.
	if !resume && l.memory != nil && userInput != "" {
		l.recallInto(ctx, &messages, userInput)
	}

	// --- Lessons recall (F3, optional) ---
	// The control plane distils lessons from finished runs; a fresh agent run
	// starts from them. Read-only and gated (self-feedback guard included),
	// and never fatal: a broken lessons store must not stop a run.
	if !resume && l.lessons != nil && userInput != "" {
		l.recallLessonsInto(ctx, &messages, sessionID, userInput)
	}

	// --- Run journal (D-12): record where this logical run begins. ---
	// Everything the loop writes from here on is appended relative to this
	// snapshot: journalBase is how far the messages have been journaled, and
	// each step reports the delta beyond it.
	if err := l.emitRunStart(ctx, sessionID, resume, cp != nil, resumeSource, userInput, messages, startStep); err != nil {
		return nil, err
	}
	journalBase := len(messages)

	// Native function calling (N2): requested by config, enabled only when the
	// client actually speaks the protocol, and switchable off mid-run when the
	// endpoint refuses it.
	nativeEnabled := l.config.NativeTools && l.router != nil
	var toolCaller ToolAwareLLM
	if nativeEnabled {
		if tc, ok := l.llm.(ToolAwareLLM); ok {
			toolCaller = tc
		} else {
			log.Printf("[harness] native tool calling requested but the LLM client does not implement ChatWithTools; using the prompt path")
			nativeEnabled = false
		}
	}

	var steps []StepRecord

	for step := startStep; step < l.config.MaxSteps; step++ {
		// --- Budget Check (M6, optional) ---
		if l.budget != nil {
			if err := l.budget.Check(ctx, sessionID); err != nil {
				l.emitRunEnded(ctx, sessionID, step, "budget_exceeded", err.Error())
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
			if !sameMessageList(messages, compacted) {
				// Compaction replaced the list wholesale (fresh slice with a
				// summarised head), so incremental turns no longer describe it.
				// Journal a snapshot and restart the delta base from there.
				if jerr := l.journalAppend(ctx, RunEvent{
					RunID: sessionID,
					Type:  EventContextCompacted,
					Step:  step,
					TS:    time.Now().UTC(),
					Data:  map[string]any{"messages": compacted},
				}); jerr != nil {
					return nil, jerr
				}
				journalBase = len(compacted)
			}
			messages = compacted
		}

		// --- Step begins ---
		if jerr := l.journalAppend(ctx, RunEvent{
			RunID: sessionID, Type: EventStepStarted, Step: step, TS: time.Now().UTC(),
		}); jerr != nil {
			return nil, jerr
		}

		// --- Water-line reminder (N2c): one relative-value line per threshold
		// per run, injected only on the crossing step — the exact figures stay
		// one context.remaining call away, and repeating them would cost more
		// than they inform.
		if usage := l.usage(messages); true {
			if level := l.reminderLevel(usage); level != "" {
				messages = append(messages, core.Message{
					Role:    "user",
					Content: contextReminderLine(usage, level),
				})
			}
		}

		// --- Call LLM ---
		// Native mode is a request, not a promise: if the endpoint refuses the
		// tools field the run degrades to the prompt path once (the client
		// remembers the refusal) and continues this very step without it.
		var chatResult core.ChatResult
		callModel := func() error {
			var callErr error
			if nativeEnabled {
				chatResult, callErr = toolCaller.ChatWithTools(ctx, messages, l.router.registry.ListTools())
				if errors.Is(callErr, ErrToolsUnsupported) {
					log.Printf("[harness] endpoint does not support native tool calling (%v); using the prompt path for the rest of this run", callErr)
					nativeEnabled = false
					chatResult, callErr = l.llm.ChatWithUsage(ctx, messages)
				}
			} else {
				chatResult, callErr = l.llm.ChatWithUsage(ctx, messages)
			}
			return callErr
		}

		err = callModel()
		// E5 static rule: the provider refusing the request as too large is a
		// problem the loop can solve — compact the window (archiving first)
		// and try THIS step one more time. A second overflow on the same step
		// means one shrink was not enough; carrying on would loop forever, so
		// the original error surfaces instead.
		if errors.Is(err, errContextOverflow) {
			_, compacted, cerr := l.compactNow(ctx, &messages, sessionID, step)
			if cerr == nil && compacted {
				journalBase = len(messages)
				log.Printf("[harness] context overflow on step %d: compacted, retrying the call once", step)
				err = callModel()
			} else {
				log.Printf("[harness] context overflow on step %d: cannot compact (%v); surfacing the error", step, cerr)
			}
		}
		if err != nil {
			// Keep the steps accumulated so far. Returning nil here discarded
			// the entire trace and made failures impossible to diagnose.
			l.emitRunEnded(ctx, sessionID, step, "error", err.Error())
			return &RunResult{Steps: steps, Reason: "error"},
				fmt.Errorf("step %d: LLM call failed: %w", step, err)
		}

		// --- LLM usage audit (D-12) ---
		if jerr := l.journalAppend(ctx, RunEvent{
			RunID: sessionID,
			Type:  EventLLMCall,
			Step:  step,
			TS:    time.Now().UTC(),
			Data: map[string]any{
				"finish_reason":     chatResult.FinishReason,
				"prompt_tokens":     chatResult.Usage.PromptTokens,
				"completion_tokens": chatResult.Usage.CompletionTokens,
				"total_tokens":      chatResult.Usage.TotalTokens,
			},
		}); jerr != nil {
			return nil, jerr
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
			l.emitRunEnded(ctx, sessionID, step, "truncated",
				fmt.Sprintf("finish_reason=length, %d completion tokens", chatResult.Usage.CompletionTokens))
			return &RunResult{Steps: steps, Reason: "truncated"},
				fmt.Errorf("step %d: LLM response truncated (finish_reason=length, %d completion tokens); "+
					"raise MaxTokens or lower the compaction threshold",
					step, chatResult.Usage.CompletionTokens)
		}

		raw := chatResult.Content

		// --- Native path: the provider already told us the tool calls ---
		if len(chatResult.ToolCalls) > 0 {
			pauseResult, err := l.runNativeToolBatch(ctx, sessionID, step, &messages, &steps, &ledger, &journalBase, chatResult)
			if err != nil {
				return nil, err
			}
			if pauseResult != nil {
				// The batch stopped for a human (a pause request or a refused
				// call). The run is saved and resumable; report it as such.
				pauseResult.Steps = steps
				return pauseResult, nil
			}
			// The streak check mirrors the prompt path: identical calls past
			// the threshold end the run honestly (see no_progress.go).
			if threshold := l.noProgressThreshold(); threshold > 0 && l.noProgress >= threshold {
				reason := fmt.Sprintf("%d consecutive identical tool calls", l.noProgress)
				l.emitRunEnded(ctx, sessionID, step, "no_progress", reason)
				return &RunResult{
					Answer: fmt.Sprintf(
						"Stopped: the same tool call was repeated %d times without change. "+
							"Different parameters, a different tool, or your answer would move the task forward.",
						l.noProgress),
					Steps:  steps,
					Reason: "no_progress",
				}, nil
			}
			continue
		}

		// Both paths converge here: one produces agentResp from the provider's
		// native answer, the other parses the prompt-encoded JSON contract.
		// Everything below (guard, journal, checkpoint, memory) is shared.
		var agentResp *structured.AgentResponse
		if nativeEnabled {
			if chatResult.Content == "" {
				l.emitRunEnded(ctx, sessionID, step, "error", "native response had neither tool calls nor content")
				return &RunResult{Steps: steps, Reason: "error"},
					fmt.Errorf("step %d: native response had neither tool calls nor content", step)
			}
			agentResp = &structured.AgentResponse{Answer: chatResult.Content}
		} else {
			var parseErr error
			agentResp, parseErr = structured.ParseWithRetry(raw, func(feedback string) (string, error) {
				retryMsgs := append(messages, core.Message{Role: "assistant", Content: raw})
				retryMsgs = append(retryMsgs, core.Message{Role: "user", Content: feedback})
				return l.llm.Chat(ctx, retryMsgs)
			})
			if parseErr != nil {
				return nil, fmt.Errorf("step %d: failed to parse agent response: %w", step, parseErr)
			}
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

			// The journal is the source of truth: completion lands there before
			// the checkpoint that caches it, so a crash between the two still
			// rebuilds as a finished run instead of redoing the answer step.
			if jerr := l.journalAppend(ctx, RunEvent{
				RunID: sessionID,
				Type:  EventRunCompleted,
				Step:  step,
				TS:    time.Now().UTC(),
				Data:  map[string]any{"answer": answer},
			}); jerr != nil {
				return nil, jerr
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

			// Pause is a loop meta-operation (N4): the model decided it needs a
			// human before going further. Reported as its own Reason rather
			// than as an error or an answer, because it is neither — the work
			// is unfinished and the run can continue from here.
			//
			// A pause that cannot say why is refused: the person releasing it
			// would have nothing to decide on, so the observation tells the
			// model to state a reason instead of parking the run.
			if toolName == PauseToolName {
				reason := pauseReason(agentResp.Action.Params)
				if reason == "" {
					messages = append(messages, core.Message{
						Role:    "assistant",
						Content: marshalAssistantTurn(agentResp),
					})
					messages = append(messages, core.Message{
						Role: "user",
						Content: fmt.Sprintf("[%s refused]: a pause must state the 'reason' parameter — "+
							"what decision is needed and from whom. Continue, or call it again with a reason.",
							PauseToolName),
					})
					continue
				}
				return l.pauseRun(ctx, sessionID, step, messages, ledger, reason)
			}

			// Permission gate (before any side effect): the run's authority must
			// cover the stronger of what the task declared and what the tool
			// does. A refusal is a PAUSE, not an error — a human can approve
			// this call and the run resumes from here.
			if decision, ok := l.checkToolAuthority(toolName); !ok {
				return l.authorityPauseResult(toolName, decision)
			}

			// Context tools are loop meta-operations (N2c): they touch the
			// loop's own window, not the world, so they never enter the
			// side-effect ledger, the duplicate guard or the progress streak.
			// The assistant echo goes in FIRST so a compaction keeps the call
			// itself inside its kept tail; the observation follows in the
			// native or prompt protocol shape respectively.
			if isContextTool(toolName) {
				messages = append(messages, core.Message{
					Role:    "assistant",
					Content: marshalAssistantTurn(agentResp),
				})
				toolResult := l.runContextTool(ctx, toolName, agentResp.Action.Params, &messages, sessionID, step)
				if toolResult.Error == "" && toolName == "context.compact" {
					// The list was replaced wholesale: restart the journal
					// delta at the new snapshot so this step's delta carries
					// only the observation (rebuild treats the compaction
					// event as a replace).
					journalBase = len(messages)
				}
				messages = append(messages, core.Message{
					Role:    "user",
					Content: formatObservation(toolName, toolResult),
				})
				steps = append(steps, StepRecord{
					Step:    step,
					Thought: agentResp.Thought,
					Action:  agentResp.Action,
					Result:  toolResult,
				})
				// Same tail as a dispatched step: journal the delta, then
				// checkpoint, so a resume picks this up.
				if jerr := l.journalAppend(ctx, RunEvent{
					RunID: sessionID,
					Type:  EventStepCompleted,
					Step:  step,
					TS:    time.Now().UTC(),
					Data:  map[string]any{"turns": messages[journalBase:]},
				}); jerr != nil {
					return nil, jerr
				}
				journalBase = len(messages)
				if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
					return nil, err
				}
				continue
			}

			ledger = append(ledger, core.ToolCallRecord{
				ID:         fmt.Sprintf("%s-step-%d-%s", sessionID, step, toolName),
				StepIndex:  step,
				Tool:       toolName,
				Params:     agentResp.Action.Params,
				Idempotent: l.toolIsIdempotent(toolName),
				Status:     core.ToolCallStarted,
				StartedAt:  time.Now().UTC(),
			})
			// Record the intent in the journal first, then the checkpoint: the
			// journal is what a rebuild trusts, so it must never be the copy
			// that misses the intent. The turn rides along so a rebuild can
			// reconstruct this step's assistant message even if the step never
			// completes.
			if jerr := l.journalAppend(ctx, RunEvent{
				RunID: sessionID,
				Type:  EventToolStarted,
				Step:  step,
				Tool:  toolName,
				TS:    ledger[len(ledger)-1].StartedAt,
				Data: map[string]any{
					"id":         ledger[len(ledger)-1].ID,
					"idempotent": ledger[len(ledger)-1].Idempotent,
					"params":     agentResp.Action.Params,
					"turn":       marshalAssistantTurn(agentResp),
				},
			}); jerr != nil {
				return nil, jerr
			}
			// Record the intent before invoking the tool. A crash between here and
			// the completion record below is exactly the case a resume has to be
			// able to see, and it can only see it if it was written first.
			if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
				return nil, err
			}

			// Duplicate guard: a non-idempotent call whose identical twin
			// already ran in this run is refused BEFORE the handler — replaying
			// its side effect is the one thing the ledger exists to prevent.
			// The refusal is an observation the model can act on, and the
			// fingerprint feeds the streak either way.
			fingerprint := toolFingerprint(toolName, agentResp.Action.Params)
			streak := l.noteProgress(fingerprint)
			var toolResult *core.ToolResult
			if !ledger[len(ledger)-1].Idempotent && l.nonIdempotentDone[fingerprint] {
				toolResult = &core.ToolResult{
					Error: fmt.Sprintf(duplicateRefusalFormat, toolName, compactParams(agentResp.Action.Params)),
				}
			} else {
				toolResult = l.router.Call(ctx, toolName, agentResp.Action.Params)
				if !ledger[len(ledger)-1].Idempotent {
					l.nonIdempotentDone[fingerprint] = true
				}
			}

			ledger[len(ledger)-1].Status = core.ToolCallCompleted
			ledger[len(ledger)-1].Result = toolResult.Output
			ledger[len(ledger)-1].Error = toolResult.Error
			ledger[len(ledger)-1].CompletedAt = time.Now().UTC()

			if jerr := l.journalAppend(ctx, RunEvent{
				RunID: sessionID,
				Type:  EventToolCompleted,
				Step:  step,
				Tool:  toolName,
				TS:    ledger[len(ledger)-1].CompletedAt,
				Data: map[string]any{
					"id":     ledger[len(ledger)-1].ID,
					"result": toolResult.Output,
					"error":  toolResult.Error,
				},
			}); jerr != nil {
				return nil, jerr
			}

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

			// Format tool result as observation. The output is untrusted
			// content, so it is screened here — before it becomes a user-role
			// message — exactly like user input. The ledger and journal above
			// already recorded the raw output: the audit trail keeps the
			// truth, only the model-facing copy is replaced.
			observationResult := toolResult
			if l.toolOutputGuard != nil && toolResult.Output != "" {
				if gerr := l.toolOutputGuard.Check(ctx, toolResult.Output); gerr != nil {
					if l.effectPolicy == EffectStrict {
						return nil, fmt.Errorf("step %d: tool output guard blocked %q output: %w", step, toolName, gerr)
					}
					log.Printf("[harness] tool output guard blocked %q output: %v", toolName, gerr)
					screened := *toolResult
					screened.Output = fmt.Sprintf("[tool output blocked by guard: %v]", gerr)
					observationResult = &screened
				}
			}
			observation := formatObservation(agentResp.Action.Name, observationResult)
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

			// Journal the finished step before it is checkpointed: the delta
			// covers every message this step appended (turn, observation,
			// reflection, verification), which is exactly what a rebuild needs
			// to reproduce the conversation.
			if jerr := l.journalAppend(ctx, RunEvent{
				RunID: sessionID,
				Type:  EventStepCompleted,
				Step:  step,
				TS:    time.Now().UTC(),
				Data:  map[string]any{"turns": messages[journalBase:]},
			}); jerr != nil {
				return nil, jerr
			}
			journalBase = len(messages)

			// Persist the finished step. This is the checkpoint a resume loads.
			if err := l.saveCheckpoint(ctx, sessionID, step, messages, ledger); err != nil {
				return nil, err
			}

			// --- No progress: the same call, again, past the threshold ---
			// Checked AFTER the step is journaled and checkpointed, so the
			// refusal that tripped it is fully on the record — the run stops
			// with everything it did, exactly like max_steps.
			if threshold := l.noProgressThreshold(); threshold > 0 && streak >= threshold {
				reason := fmt.Sprintf("%d consecutive identical tool calls (%s)", streak, toolName)
				l.emitRunEnded(ctx, sessionID, step, "no_progress", reason)
				return &RunResult{
					Answer: fmt.Sprintf(
						"Stopped: the same tool call %s was repeated %d times without change. "+
							"Nothing new can come from repeating it — different parameters, a different tool, "+
							"or your answer would move the task forward.",
						toolName, streak),
					Steps:  steps,
					Reason: "no_progress",
				}, nil
			}

			continue
		}

		// Should not reach here — Validate() ensures one of the two paths.
		return nil, fmt.Errorf("step %d: agent response is neither terminal nor tool call", step)
	}

	// --- Max steps exceeded ---
	l.emitRunEnded(ctx, sessionID, l.config.MaxSteps, "max_steps", "")
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

// sameMessageList reports whether a and b are the same underlying message
// list. CompactIfNeeded returns its input unchanged when nothing happened and
// a freshly built slice when it compacted, which is exactly what identity of
// the first element distinguishes (both lists always start with a system
// message, so a non-empty list is the normal case).
func sameMessageList(a, b []core.Message) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return &a[0] == &b[0]
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
