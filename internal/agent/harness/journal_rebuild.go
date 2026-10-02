package harness

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// RebuildCheckpoint reconstructs a resumable checkpoint from journal events.
//
// This is the journal's read side and the counterpart of the write-side
// fallback: writes may never be silent about loss, and a read that finds
// loss refuses instead of rebuilding a state that quietly omits a tool that
// already ran. The rules:
//
//   - the logical run starts at the last run_started/run_resumed; earlier
//     runs in the same journal are audit, not state;
//   - from that scope on, seq must be contiguous — a jump or an explicit
//     journal_gap marker means loss, and loss means ErrJournalGap;
//   - messages rebuild from the scope's snapshot plus each step's recorded
//     message delta (or a compaction snapshot when one replaced the list);
//   - a step whose tool finished but whose step_completed never landed gets
//     its assistant turn and observation reconstructed from the tool events —
//     redoing the step would repeat a side effect that already happened;
//   - a tool that started but never completed stays dangling in the ledger,
//     which is exactly the state D-06's resume rules know how to handle
//     (refuse non-idempotent, redo idempotent).
//
// ErrNoJournalEvents means "nothing to rebuild" and is a normal first call.
// Every other error means "do not run".
func RebuildCheckpoint(events []RunEvent) (*core.Checkpoint, error) {
	if len(events) == 0 {
		return nil, ErrNoJournalEvents
	}
	sortEvents(events)

	scope := -1
	for i, ev := range events {
		if ev.Type == EventRunStarted || ev.Type == EventRunResumed {
			scope = i
		}
	}
	if scope < 0 {
		return nil, fmt.Errorf("%w: events exist but the run start is missing", ErrJournalGap)
	}
	scopeEv := events[scope]

	prev := scopeEv.Seq
	for _, ev := range events[scope+1:] {
		if ev.Type == EventJournalGap {
			return nil, fmt.Errorf("%w: marker records loss of seq %v..%v", ErrJournalGap,
				ev.Data["gap_from"], ev.Data["gap_to"])
		}
		if ev.Seq != prev+1 {
			return nil, fmt.Errorf("%w: sequence jumps from %d to %d", ErrJournalGap, prev, ev.Seq)
		}
		prev = ev.Seq
	}

	messages, err := decodeMessages(scopeEv.Data["messages"])
	if err != nil {
		return nil, fmt.Errorf("rebuild: run start snapshot: %w", err)
	}
	stepIndex := -1
	if scopeEv.Type == EventRunResumed {
		stepIndex = asInt(scopeEv.Data["step_index"])
	}

	var (
		ledger      []core.ToolCallRecord
		completed   bool
		answer      string
		lastStep    = stepIndex
		toolTurns   = map[int]string{}
		toolResults = map[int]*core.ToolResult{}
		toolNames   = map[int]string{}
	)

	for _, ev := range events[scope+1:] {
		switch ev.Type {
		case EventContextCompacted:
			// A compaction replaced the list wholesale; the snapshot is the
			// new delta base.
			messages, err = decodeMessages(ev.Data["messages"])
			if err != nil {
				return nil, fmt.Errorf("rebuild: compaction snapshot at seq %d: %w", ev.Seq, err)
			}
		case EventToolStarted:
			ledger = append(ledger, core.ToolCallRecord{
				ID:         asString(ev.Data["id"]),
				StepIndex:  ev.Step,
				Tool:       ev.Tool,
				Idempotent: asBool(ev.Data["idempotent"]),
				Status:     core.ToolCallStarted,
				StartedAt:  ev.TS,
			})
			toolTurns[ev.Step] = asString(ev.Data["turn"])
			toolNames[ev.Step] = ev.Tool
		case EventToolCompleted:
			for i := len(ledger) - 1; i >= 0; i-- {
				if ledger[i].ID == asString(ev.Data["id"]) {
					ledger[i].Status = core.ToolCallCompleted
					ledger[i].Result = asString(ev.Data["result"])
					ledger[i].Error = asString(ev.Data["error"])
					ledger[i].CompletedAt = ev.TS
					break
				}
			}
			toolResults[ev.Step] = &core.ToolResult{
				Output: asString(ev.Data["result"]),
				Error:  asString(ev.Data["error"]),
			}
			toolNames[ev.Step] = ev.Tool
		case EventStepCompleted:
			turns, err := decodeMessages(ev.Data["turns"])
			if err != nil {
				return nil, fmt.Errorf("rebuild: step %d turns: %w", ev.Step, err)
			}
			messages = append(messages, turns...)
			lastStep = ev.Step
			delete(toolTurns, ev.Step)
			delete(toolResults, ev.Step)
		case EventRunCompleted:
			completed = true
			answer = asString(ev.Data["answer"])
		}
	}

	// A tool that completed while its step never did: reconstruct the two
	// messages the loop would have appended, so the run continues past the
	// step instead of repeating the tool.
	if !completed && lastStep+1 >= 0 {
		p := lastStep + 1
		if result, ok := toolResults[p]; ok {
			if turn := toolTurns[p]; turn != "" {
				messages = append(messages, core.Message{Role: "assistant", Content: turn})
			}
			messages = append(messages, core.Message{
				Role:    "user",
				Content: formatObservation(toolNames[p], result),
			})
			lastStep = p
		}
	}

	return &core.Checkpoint{
		ID:        fmt.Sprintf("%s-step-%d", scopeEv.RunID, lastStep),
		SessionID: scopeEv.RunID,
		StepIndex: lastStep,
		Messages:  messages,
		ToolCalls: ledger,
		Completed: completed,
		Answer:    answer,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// decodeMessages reads a []core.Message snapshot that travelled through the
// journal as generic JSON.
func decodeMessages(v any) ([]core.Message, error) {
	if v == nil {
		return nil, fmt.Errorf("snapshot missing")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var msgs []core.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// asString/asBool/asInt read Data fields after a JSON round trip, where
// every number arrived as float64 and every shape as map[string]any.
func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return -1
	}
}
