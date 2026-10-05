package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

// sseServer answers every request with the given SSE body, flushing each
// frame so the client really reads incrementally.
func sseServer(t *testing.T, frames []string, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			fmt.Fprint(w, f)
			if flusher != nil {
				flusher.Flush()
			}
			if delay > 0 {
				time.Sleep(delay)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamClient(t *testing.T, srv *httptest.Server, timeout time.Duration) *LLMClient {
	t.Helper()
	cfg := DefaultLLMConfig()
	cfg.BaseURL = srv.URL
	cfg.MaxRetries = 0
	cfg.Timeout = timeout
	cfg.Streaming = true
	return NewLLMClient(cfg)
}

func dataFrame(payload string) string { return "data: " + payload + "\n\n" }

// The happy path: content frames accumulate, the usage trailer lands, and the
// [DONE] sentinel ends the read.
func TestStreamAccumulatesContentAndUsage(t *testing.T) {
	srv := sseServer(t, []string{
		dataFrame(`{"choices":[{"delta":{"content":"Hel"}}]}`),
		dataFrame(`{"choices":[{"delta":{"content":"lo "}}]}`),
		dataFrame(`{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`),
		dataFrame(`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`),
		dataFrame(`[DONE]`),
	}, 0)

	client := streamClient(t, srv, 5*time.Second)
	result, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "hi"}})
	require.NoError(t, err)

	assert.Equal(t, "Hello world", result.Content)
	assert.Equal(t, "stop", result.FinishReason)
	assert.Equal(t, 10, result.Usage.TotalTokens, "the usage trailer is captured")
	assert.Empty(t, result.ToolCalls)
}

// Tool calls arrive as fragments: one frame carries the name, later frames
// carry argument pieces. The result must be one complete, parsed call.
func TestStreamAggregatesToolCallFragments(t *testing.T) {
	srv := sseServer(t, []string{
		dataFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"test.push","arguments":"{\"x\":"}}]}}]}`),
		dataFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"42}"}}]}}]}`),
		dataFrame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		dataFrame(`[DONE]`),
	}, 0)

	client := streamClient(t, srv, 5*time.Second)
	result, err := client.ChatWithTools(context.Background(),
		[]core.Message{{Role: "user", Content: "go"}}, testToolDefs())
	require.NoError(t, err)

	require.Len(t, result.ToolCalls, 1)
	call := result.ToolCalls[0]
	assert.Equal(t, "call_a", call.ID)
	assert.Equal(t, "test.push", call.Name)
	assert.Equal(t, float64(42), call.Arguments["x"], "fragments were spliced and parsed")
	assert.Equal(t, "tool_calls", result.FinishReason)
}

// The bug streaming exists to fix: a slow answer that would have died at the
// old whole-response timeout now completes, because every frame restarts the
// watchdog.
func TestStreamSurvivesSlowFlowingAnswer(t *testing.T) {
	// Six frames, 40ms apart = 200ms of wall time, with a 60ms watchdog: only
	// per-frame renewal can carry this to completion.
	frames := make([]string, 0, 8)
	for i := 0; i < 6; i++ {
		frames = append(frames, dataFrame(fmt.Sprintf(`{"choices":[{"delta":{"content":"%d"}}]}`, i)))
	}
	frames = append(frames, dataFrame(`[DONE]`))

	srv := sseServer(t, frames, 40*time.Millisecond)
	client := streamClient(t, srv, 60*time.Millisecond)

	result, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "slow"}})
	require.NoError(t, err, "a flowing answer must not be killed by the per-frame interval")
	assert.Equal(t, "012345", result.Content)
}

// Silence is still fatal: a stalled connection must not hang forever.
//
// Tested at the transport, not through the client: a stall is retryable by
// design (decision 4), so the client-level path would spend the whole retry
// budget (1s+2s+4s) exercising backoff rather than the watchdog. The
// retryable-ness itself is asserted directly below.
func TestStreamStallIsRetryable(t *testing.T) {
	release := make(chan struct{})
	reader := &blockingReader{release: release}
	t.Cleanup(func() { close(release) })

	err := readStream(context.Background(), reader, newStreamAccumulator(), 50*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStreamStalled)
	assert.True(t, isRetryable(err), "a stall is worth retrying the whole request")
}

// A stream that ends without [DONE] is a fragment, never a complete answer.
func TestStreamWithoutDoneSentinelIsRejected(t *testing.T) {
	body := dataFrame(`{"choices":[{"delta":{"content":"half an ans"}}]}`)
	err := readStream(context.Background(), strings.NewReader(body), newStreamAccumulator(), time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStreamIncomplete)
	assert.True(t, isRetryable(err), "a truncated stream must be re-requested, not presented as an answer")
}

// blockingReader never yields bytes until released — a connection that is
// open but silent.
type blockingReader struct{ release chan struct{} }

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.release
	return 0, fmt.Errorf("released")
}

// An endpoint that answers whole JSON despite stream:true is tolerated: the
// buffered decode takes over (decision 7).
func TestStreamToleratesNonSSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"buffered answer"},"finish_reason":"stop"}],"usage":{"total_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)

	client := streamClient(t, srv, 5*time.Second)
	result, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "x"}})
	require.NoError(t, err)
	assert.Equal(t, "buffered answer", result.Content)
	assert.Equal(t, 5, result.Usage.TotalTokens)
}

// An endpoint that refuses the stream field latches it off and re-runs the
// same call on the buffered body — one request lost, then normal service.
func TestStreamParamRejectionFallsBackImmediately(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		text := string(body[:n])

		if strings.Contains(text, `"stream":true`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"unknown parameter: stream"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	t.Cleanup(srv.Close)

	client := streamClient(t, srv, 5*time.Second)
	result, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "x"}})
	require.NoError(t, err, "the stream-field refusal must be absorbed, not surfaced")
	assert.Equal(t, "ok", result.Content)
	assert.Equal(t, 2, requests, "one refused streaming attempt, one buffered retry")

	// Latched: the next call never tries streaming again.
	before := requests
	_, err = client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "y"}})
	require.NoError(t, err)
	assert.Equal(t, before+1, requests, "later calls go straight to the buffered body")
}

// Streaming off must keep the historical wire shape: no stream fields at all.
func TestStreamingDisabledSendsNoStreamFields(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		captured = string(body[:n])
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	t.Cleanup(srv.Close)

	cfg := DefaultLLMConfig()
	cfg.BaseURL = srv.URL
	cfg.MaxRetries = 0
	cfg.Streaming = false
	client := NewLLMClient(cfg)

	_, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "x"}})
	require.NoError(t, err)
	assert.NotContains(t, captured, `"stream"`, "streaming off means the field never appears")
}

// A provider error frame mid-stream is a real error, not a parse failure.
func TestStreamErrorFrameIsSurfaced(t *testing.T) {
	srv := sseServer(t, []string{
		dataFrame(`{"choices":[{"delta":{"content":"partial"}}]}`),
		dataFrame(`{"error":{"message":"quota exceeded"}}`),
	}, 0)

	client := streamClient(t, srv, 5*time.Second)
	_, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "x"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

// --- parser units (no HTTP) ---

func TestStreamAccumulatorIgnoresHeartbeats(t *testing.T) {
	acc := newStreamAccumulator()
	require.NoError(t, acc.apply(&streamChunk{}))
	result, err := acc.result()
	require.NoError(t, err)
	assert.Empty(t, result.Content)
	assert.Empty(t, result.ToolCalls)
}

func TestStreamAccumulatorNamesBadArgumentJSON(t *testing.T) {
	acc := newStreamAccumulator()
	var frame streamChunk
	require.NoError(t, json.Unmarshal([]byte(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"test.push","arguments":"{nope"}}]}}]}`),
		&frame))
	require.NoError(t, acc.apply(&frame))

	_, err := acc.result()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test.push")
	assert.Contains(t, err.Error(), "not valid JSON")
}
