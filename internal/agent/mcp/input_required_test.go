package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inputServerTransport answers tools/call from a script of results, recording
// every request so the retry loop can be inspected.
type inputServerTransport struct {
	// results are returned in order for tools/call.
	results []string
	calls   []map[string]any
}

func (s *inputServerTransport) Send(_ context.Context, req Request) (Response, error) {
	switch req.Method {
	case "server/discover":
		raw, err := json.Marshal(discoverResult{ProtocolVersions: []string{ProtocolModern}})
		if err != nil {
			return Response{}, err
		}
		return Response{ID: req.ID, Result: raw}, nil
	case "tools/call":
		var params map[string]any
		_ = json.Unmarshal(req.Params, &params)
		s.calls = append(s.calls, params)
		idx := len(s.calls) - 1
		if idx >= len(s.results) {
			idx = len(s.results) - 1
		}
		return Response{ID: req.ID, Result: json.RawMessage(s.results[idx])}, nil
	}
	return Response{ID: req.ID, Error: &ResponseError{Code: CodeMethodNotFound}}, nil
}

func (s *inputServerTransport) Notify(context.Context, Notification) error { return nil }
func (s *inputServerTransport) Close() error                               { return nil }

func inputClient(t *testing.T, results ...string) (*Client, *inputServerTransport) {
	t.Helper()
	tr := &inputServerTransport{results: results}
	client, err := NewClient(context.Background(), tr)
	require.NoError(t, err)
	return client, tr
}

// THE bug this whole file exists for: a compliant server's InputRequiredResult
// used to parse as "no content, no error" — the caller received an empty string
// and the model read it as "the tool returned nothing". It must never do that
// again, whichever way it is handled.
func TestInputRequiredIsNeverAnEmptySuccess(t *testing.T) {
	client, _ := inputClient(t, `{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create"}}}`)

	out, err := client.CallTool(context.Background(), "remote.tool", json.RawMessage(`{}`))

	require.Error(t, err, "an unanswerable input request is an error, never an empty success")
	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "elicitation/create", "the error names what was asked for")
}

// The default handler refuses by design, and the refusal is distinguishable
// from a handler failure: sampling and roots are deprecated, so refusing is the
// correct answer, not a bug to be retried.
func TestDefaultHandlerRefusesWithSentinel(t *testing.T) {
	client, _ := inputClient(t, `{"resultType":"input_required","inputRequests":{"s":{"method":"sampling/createMessage"}}}`)

	_, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInputUnsupported, "the refusal is the documented sentinel")
}

// A server that can be answered is answered, and the call RETRIES with the
// answers — the flow the specification describes.
func TestInputRequiredIsAnsweredAndRetried(t *testing.T) {
	client, tr := inputClient(t,
		`{"resultType":"input_required","inputRequests":{"login":{"method":"elicitation/create","params":{"message":"username?"}}},"requestState":"opaque-1"}`,
		`{"content":[{"type":"text","text":"done"}],"isError":false}`,
	)
	client.SetInputHandler(func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		assert.Equal(t, "elicitation/create", method)
		return map[string]any{"action": "accept", "content": map[string]any{"name": "octocat"}}, nil
	})

	out, err := client.CallTool(context.Background(), "remote.tool", json.RawMessage(`{"a":1}`))
	require.NoError(t, err)
	assert.Equal(t, "done", out)

	require.Len(t, tr.calls, 2, "the call was retried once")

	// The retry carries the answers AND the opaque state, untouched.
	second := tr.calls[1]
	responses, ok := second["inputResponses"].(map[string]any)
	require.True(t, ok, "the retry carries inputResponses: %v", second)
	assert.Contains(t, responses, "login")
	assert.Equal(t, "opaque-1", second["requestState"], "requestState is echoed back verbatim")
	// The original arguments still travel: a retry is the same call plus answers.
	assert.Equal(t, "remote.tool", second["name"])
}

// Several requests in one round are all answered, in a deterministic order, and
// one refusal aborts the whole call — a partially answered set would be sent as
// if it were complete.
func TestMultipleInputRequestsAndFirstFailureAborts(t *testing.T) {
	client, tr := inputClient(t,
		`{"resultType":"input_required","inputRequests":{
			"b":{"method":"elicitation/create"},
			"a":{"method":"elicitation/create"},
			"c":{"method":"sampling/createMessage"}
		}}`,
	)

	var asked []string
	client.SetInputHandler(func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		asked = append(asked, method)
		if method == "sampling/createMessage" {
			return nil, ErrInputUnsupported
		}
		return map[string]any{"ok": true}, nil
	})

	_, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.Error(t, err, "one unanswerable request fails the call")

	// Sorted order means "a" and "b" are attempted before "c"; the failure at
	// "c" stops the loop, so nothing is sent.
	assert.Equal(t, []string{"elicitation/create", "elicitation/create", "sampling/createMessage"}, asked)
	assert.Len(t, tr.calls, 1, "no retry is sent with a partial answer set")
}

// A server that keeps asking without ever completing is bounded, not looped.
func TestInputRequiredLoopIsBounded(t *testing.T) {
	client, tr := inputClient(t,
		`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create"}}}`,
	)
	client.SetInputHandler(func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})

	_, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without completing")
	// attempt 0 plus maxInputRetries retries.
	assert.Len(t, tr.calls, maxInputRetries+1)
}

// input_required with no requests listed cannot be progressed: retrying
// identical bytes would loop, so it is reported.
func TestInputRequiredWithoutRequestsIsReported(t *testing.T) {
	client, tr := inputClient(t, `{"resultType":"input_required"}`)

	_, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listed no requests")
	assert.Len(t, tr.calls, 1, "no pointless retry")
}

// A normal result still works exactly as before: the H4 path must not change
// the ordinary case.
func TestOrdinaryResultStillWorks(t *testing.T) {
	client, tr := inputClient(t, `{"content":[{"type":"text","text":"hello"}],"isError":false}`)

	out, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
	assert.Len(t, tr.calls, 1, "one call, no round trips")

	// The ordinary request carries no inputResponses.
	assert.NotContains(t, tr.calls[0], "inputResponses")
}

// An error result is still an error.
func TestErrorResultStillErrors(t *testing.T) {
	client, _ := inputClient(t, `{"content":[{"type":"text","text":"boom"}],"isError":true}`)

	_, err := client.CallTool(context.Background(), "remote.tool", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// isInputRequired is the small helper the shape check rests on.
func TestIsInputRequired(t *testing.T) {
	assert.True(t, isInputRequired(json.RawMessage(`{"resultType":"input_required"}`)))
	assert.False(t, isInputRequired(json.RawMessage(`{"resultType":"complete"}`)))
	assert.False(t, isInputRequired(json.RawMessage(`{"content":[]}`)))
	assert.False(t, isInputRequired(json.RawMessage(`not json`)))
}

// The refusal message names the method, so an operator can see what the server
// wanted and decide whether to wire a handler.
func TestRefusalNamesTheMethod(t *testing.T) {
	_, err := defaultInputHandler(context.Background(), "roots/list", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roots/list")
	assert.True(t, strings.Contains(err.Error(), ErrInputUnsupported.Error()))
}
