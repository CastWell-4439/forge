package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/agent/core"
)

func echoServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func testClient(srv *httptest.Server) *LLMClient {
	cfg := DefaultLLMConfig()
	cfg.BaseURL = srv.URL
	cfg.MaxRetries = 0 // tests must not sleep on retries
	return NewLLMClient(cfg)
}

// B1+B5 on the wire: the request carries the tools array, and each tool's
// parameters are a real JSON Schema with descriptions and required fields —
// the model finally sees fields as fields.
func TestNativeRequestCarriesToolsAndSchema(t *testing.T) {
	var captured map[string]any
	srv, _ := echoServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"test.push","arguments":"{\"x\":1}"}},
			{"id":"call_2","type":"function","function":{"name":"test.lookup","arguments":"{\"q\":\"y\"}"}}
		]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	})

	client := testClient(srv)
	result, err := client.ChatWithTools(context.Background(),
		[]core.Message{{Role: "user", Content: "go"}}, testToolDefs())
	require.NoError(t, err)

	// Request shape.
	tools, ok := captured["tools"].([]any)
	require.True(t, ok, "the request must carry tools")
	require.Len(t, tools, 2)
	tool0 := tools[0].(map[string]any)
	assert.Equal(t, "function", tool0["type"])
	fn := tool0["function"].(map[string]any)
	assert.Equal(t, "test.push", fn["name"])
	schema := fn["parameters"].(map[string]any)
	assert.Equal(t, "object", schema["type"])
	props := schema["properties"].(map[string]any)
	require.Contains(t, props, "x")
	x := props["x"].(map[string]any)
	assert.Equal(t, "integer", x["type"], "the parameter type reaches the wire")
	assert.NotEmpty(t, x["description"], "the parameter description reaches the wire")
	assert.Contains(t, schema["required"], "x")

	// Response parsing: both calls, arguments as real objects.
	require.Len(t, result.ToolCalls, 2)
	assert.Equal(t, "call_1", result.ToolCalls[0].ID)
	assert.Equal(t, "test.push", result.ToolCalls[0].Name)
	assert.Equal(t, float64(1), result.ToolCalls[0].Arguments["x"])
	assert.Equal(t, "tool_calls", result.FinishReason)
	assert.Equal(t, 15, result.Usage.TotalTokens, "usage accounting survives the native path")
}

// The endpoint refusing the tools field degrades once: sentinel returned,
// attempts latched off (no repeat doomed requests), and the prompt-shaped
// calls keep working.
func TestNativeUnsupportedLatchesAndFallsBack(t *testing.T) {
	srv, counter := echoServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"unknown parameter: tools","type":"invalid_request_error"}}`)
	})

	client := testClient(srv)
	_, err := client.ChatWithTools(context.Background(), nil, testToolDefs())
	require.ErrorIs(t, err, ErrToolsUnsupported)

	// Second attempt: latched — the server is not called again.
	before := counter.Load()
	_, err = client.ChatWithTools(context.Background(), nil, testToolDefs())
	require.ErrorIs(t, err, ErrToolsUnsupported)
	assert.Equal(t, before, counter.Load(), "the latched client must not re-attempt")

	// The plain path still works against the same (tools-hostile) endpoint —
	// the prompt path never sends the field... it still gets the 400 here
	// because this fake server refuses EVERYTHING; assert only that the
	// sentinel semantics hold, not prompt success (covered elsewhere).
}

// The prompt path must stay byte-shape identical: no tools field, ever.
func TestPromptPathCarriesNoToolsField(t *testing.T) {
	var captured map[string]any
	srv, _ := echoServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`)
	})

	client := testClient(srv)
	result, err := client.ChatWithUsage(context.Background(), []core.Message{{Role: "user", Content: "q"}})
	require.NoError(t, err)
	assert.Equal(t, "hi", result.Content)
	assert.NotContains(t, captured, "tools", "the prompt path must not send the tools field")
	assert.Empty(t, result.ToolCalls)
}

// Garbage arguments from the provider are reported honestly, named by tool —
// not silently coerced into an empty parameter map.
func TestNativeBadArgumentsAreNamed(t *testing.T) {
	srv, _ := echoServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[
			{"id":"c","type":"function","function":{"name":"test.push","arguments":"{not json"}}
		]},"finish_reason":"tool_calls"}],"usage":{}}`)
	})

	client := testClient(srv)
	_, err := client.ChatWithTools(context.Background(), nil, testToolDefs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test.push")
	assert.Contains(t, err.Error(), "not valid JSON")
}

// testToolDefs returns a small two-tool set for schema assertions.
func testToolDefs() []*core.ToolDef {
	return []*core.ToolDef{
		{
			Name:        "test.push",
			Description: "Push a value",
			InputSchema: map[string]core.ParamDef{
				"x": {Type: "integer", Description: "the value to push", Required: true},
			},
		},
		{
			Name:        "test.lookup",
			Description: "Look up a value",
			InputSchema: map[string]core.ParamDef{
				"q": {Type: "string", Description: "query", Required: true},
			},
			RequiredParams: []string{"q"},
		},
	}
}
