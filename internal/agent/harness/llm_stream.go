package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/agent/core"
)

// Streaming transport (N2b / B2): the request says stream:true and the
// response arrives as server-sent events; this file parses them into the
// same core.ChatResult the buffered path produces.
//
// What streaming buys here, in order of importance:
//  1. Timeout semantics — the old client timeout capped the WHOLE response,
//     so a long generation was killed at 60s. Streaming switches to a read
//     watchdog: every data line restarts the clock (decision 3), so a flowing
//     answer lives as long as it keeps flowing while a silent connection
//     still fails.
//  2. A complete SSE implementation, including tool_calls fragments.
//  3. The seam a future progress display would use — no consumer exists yet,
//     so nothing outside this file changes (decision 1).

// streamOptions asks the provider for the trailing usage chunk.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// errStreamStalled marks "no data arrived within the watchdog interval" —
// transient, worth a retry of the whole request (decision 4).
var errStreamStalled = fmt.Errorf("stream stalled: no data within the read interval")

// errStreamIncomplete marks "connection ended without the [DONE] sentinel" —
// the accumulated text may be a fragment, and a fragment must never be
// presented as a complete answer.
var errStreamIncomplete = fmt.Errorf("stream ended without a done sentinel; the answer may be incomplete")

// streamChunk mirrors one SSE data payload.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamToolCall accumulates one tool call's frames: id, name and the
// argument text may arrive split across chunks (decision 6).
type streamToolCall struct {
	ID   string
	Name string
	Args string
}

// streamAccumulator merges streamed frames into the shared result shape.
type streamAccumulator struct {
	content      strings.Builder
	finishReason string
	usage        core.TokenUsage
	calls        map[int]*streamToolCall
}

func newStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{calls: map[int]*streamToolCall{}}
}

// apply merges one decoded frame.
func (a *streamAccumulator) apply(c *streamChunk) error {
	if c.Error != nil {
		// The provider reported a failure mid-stream: a real error, not a
		// parse problem.
		return fmt.Errorf("LLM stream error: %s", c.Error.Message)
	}
	for _, choice := range c.Choices {
		a.content.WriteString(choice.Delta.Content)
		if choice.FinishReason != "" {
			a.finishReason = choice.FinishReason
		}
		for _, call := range choice.Delta.ToolCalls {
			idx := len(a.calls)
			if call.Index != nil {
				idx = *call.Index
			}
			tc, ok := a.calls[idx]
			if !ok {
				tc = &streamToolCall{}
				a.calls[idx] = tc
			}
			if call.ID != "" {
				tc.ID = call.ID
			}
			if call.Function.Name != "" {
				tc.Name = call.Function.Name
			}
			tc.Args += call.Function.Arguments
		}
	}
	if c.Usage != nil {
		a.usage = core.TokenUsage{
			PromptTokens:     c.Usage.PromptTokens,
			CompletionTokens: c.Usage.CompletionTokens,
			TotalTokens:      c.Usage.TotalTokens,
		}
	}
	return nil
}

// result assembles the accumulation into the shared ChatResult. Argument
// text is validated exactly like the buffered path: garbage arguments are a
// named error, never silently coerced into an empty map.
func (a *streamAccumulator) result() (core.ChatResult, error) {
	result := core.ChatResult{
		Content:      a.content.String(),
		FinishReason: a.finishReason,
		Usage:        a.usage,
	}

	indexes := make([]int, 0, len(a.calls))
	for idx := range a.calls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes) // wire order, whatever order frames named them

	for _, idx := range indexes {
		tc := a.calls[idx]
		arguments := map[string]any{}
		if strings.TrimSpace(tc.Args) != "" {
			if err := json.Unmarshal([]byte(tc.Args), &arguments); err != nil {
				return core.ChatResult{}, fmt.Errorf("tool call %q: arguments are not valid JSON: %w", tc.Name, err)
			}
		}
		result.ToolCalls = append(result.ToolCalls, core.NativeToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: arguments,
		})
	}
	return result, nil
}

// readStream consumes an SSE body into the accumulator.
//
// Lines are read on a goroutine so the watchdog can observe silence: a data
// line restarts the clock; no line for `interval` means the connection is
// stalled and the read fails (decision 3's "每段续期，卡死仍超时").
// The loop returns nil only on the [DONE] sentinel.
//
// The reader goroutine is told to stop on every exit path (done channel):
// without that it would block forever on an unbuffered send after the loop
// returned early, holding the response body open and leaking a goroutine per
// stalled or rejected stream.
func readStream(ctx context.Context, body io.Reader, acc *streamAccumulator, interval time.Duration) error {
	lines := make(chan string)
	scanErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case scanErr <- err:
			case <-done:
			}
		}
	}()

	watchdog := time.NewTicker(interval)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-scanErr:
			// A read-level break mid-stream: the text so far is a fragment.
			return &retryableError{err: fmt.Errorf("stream read failed: %w", err)}

		case line, ok := <-lines:
			if !ok {
				// Clean EOF without [DONE]: reject the fragment (decision 4).
				return &retryableError{err: errStreamIncomplete}
			}
			if !strings.HasPrefix(line, "data:") {
				continue // keep-alives, comments, event: lines
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				return nil
			}
			var frame streamChunk
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				// A frame we cannot parse ends the stream as unusable.
				return fmt.Errorf("parse stream frame: %w", err)
			}
			if err := acc.apply(&frame); err != nil {
				return err
			}
			watchdog.Reset(interval)

		case <-watchdog.C:
			// Nothing arrived for one full interval: the connection stalled.
			return &retryableError{err: errStreamStalled}
		}
	}
}
