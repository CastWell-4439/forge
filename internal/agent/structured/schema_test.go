package structured

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Schema tests ---

func TestGenerateSchemaSimpleStruct(t *testing.T) {
	type Simple struct {
		Name string `json:"name" desc:"The name"`
		Age  int    `json:"age"`
	}
	schema := GenerateSchema(Simple{})
	assert.Equal(t, SchemaObject, schema.Type)
	assert.Contains(t, schema.Properties, "name")
	assert.Contains(t, schema.Properties, "age")
	assert.Equal(t, SchemaString, schema.Properties["name"].Type)
	assert.Equal(t, "The name", schema.Properties["name"].Description)
	assert.Equal(t, SchemaInteger, schema.Properties["age"].Type)
	assert.Contains(t, schema.Required, "name")
	assert.Contains(t, schema.Required, "age")
}

func TestGenerateSchemaOptionalFields(t *testing.T) {
	type WithOptional struct {
		Required string  `json:"required"`
		Optional string  `json:"optional,omitempty"`
		Pointer  *string `json:"pointer"`
	}
	schema := GenerateSchema(WithOptional{})
	assert.Contains(t, schema.Required, "required")
	assert.NotContains(t, schema.Required, "optional")
	assert.NotContains(t, schema.Required, "pointer")
}

func TestGenerateSchemaAgentResponse(t *testing.T) {
	schema := GenerateSchema(AgentResponse{})
	assert.Equal(t, SchemaObject, schema.Type)
	assert.Contains(t, schema.Properties, "thought")
	assert.Contains(t, schema.Properties, "action")
	assert.Contains(t, schema.Properties, "answer")
	assert.Contains(t, schema.Required, "thought")
	assert.NotContains(t, schema.Required, "action")
	assert.NotContains(t, schema.Required, "answer")
}

func TestGenerateSchemaPointerInput(t *testing.T) {
	schema := GenerateSchema(&AgentResponse{})
	assert.Equal(t, SchemaObject, schema.Type)
	assert.Contains(t, schema.Properties, "thought")
}

func TestFormatForLLM(t *testing.T) {
	schema := GenerateSchema(AgentResponse{})
	output := FormatForLLM(schema)
	assert.Contains(t, output, "thought")
	assert.Contains(t, output, "action")
	assert.Contains(t, output, "answer")
}

// --- Types tests ---

func TestAgentResponseIsTerminal(t *testing.T) {
	assert.True(t, (&AgentResponse{Thought: "done", Answer: "result"}).IsTerminal())
	assert.False(t, (&AgentResponse{Thought: "thinking"}).IsTerminal())
}

func TestAgentResponseIsToolCall(t *testing.T) {
	resp := &AgentResponse{
		Thought: "need to probe",
		Action:  &ToolCallRequest{Name: "video.probe", Params: map[string]interface{}{"path": "/tmp/v.mp4"}},
	}
	assert.True(t, resp.IsToolCall())
	assert.False(t, resp.IsTerminal())
}

func TestAgentResponseValidate(t *testing.T) {
	tests := []struct {
		name    string
		resp    AgentResponse
		wantErr bool
	}{
		{
			name:    "valid tool call",
			resp:    AgentResponse{Thought: "think", Action: &ToolCallRequest{Name: "tool"}},
			wantErr: false,
		},
		{
			name:    "valid answer",
			resp:    AgentResponse{Thought: "think", Answer: "done"},
			wantErr: false,
		},
		{
			name:    "missing thought",
			resp:    AgentResponse{Answer: "done"},
			wantErr: true,
		},
		{
			name:    "both action and answer",
			resp:    AgentResponse{Thought: "t", Action: &ToolCallRequest{Name: "x"}, Answer: "y"},
			wantErr: true,
		},
		{
			name:    "neither action nor answer",
			resp:    AgentResponse{Thought: "thinking"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.resp.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- Validator tests ---

func TestParseResponseValid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "tool call",
			raw:  `{"thought": "I need to probe the video", "action": {"name": "video.probe", "params": {"path": "/tmp/v.mp4"}}}`,
		},
		{
			name: "answer",
			raw:  `{"thought": "I have the result", "answer": "The video is 1080p"}`,
		},
		{
			name: "with markdown fence",
			raw:  "```json\n{\"thought\": \"test\", \"answer\": \"done\"}\n```",
		},
		{
			name: "with preamble",
			raw:  "Here is my response:\n{\"thought\": \"think\", \"answer\": \"result\"}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ParseResponse(tc.raw)
			require.NoError(t, err)
			assert.NotEmpty(t, resp.Thought)
		})
	}
}

func TestParseResponseInvalid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"no json", "this is just text"},
		{"empty json", "{}"},
		{"missing thought", `{"answer": "done"}`},
		{"both action and answer", `{"thought": "t", "action": {"name": "x"}, "answer": "y"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseResponse(tc.raw)
			assert.Error(t, err)
		})
	}
}

func TestParseWithRetry(t *testing.T) {
	callCount := 0
	retryFn := func(feedback string) (string, error) {
		callCount++
		// Second attempt returns valid JSON.
		return `{"thought": "fixed", "answer": "done"}`, nil
	}

	// First input is invalid.
	resp, err := ParseWithRetry("not json", retryFn)
	require.NoError(t, err)
	assert.Equal(t, "fixed", resp.Thought)
	assert.Equal(t, "done", resp.Answer)
	assert.Equal(t, 1, callCount)
}

func TestParseWithRetryAllFail(t *testing.T) {
	retryFn := func(feedback string) (string, error) {
		return "still not json", nil
	}

	_, err := ParseWithRetry("not json", retryFn)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse after")
}

func TestParseWithRetryFnError(t *testing.T) {
	retryFn := func(feedback string) (string, error) {
		return "", fmt.Errorf("LLM API error")
	}

	_, err := ParseWithRetry("not json", retryFn)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "retry 1 failed")
}

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"pure json", `{"a": 1}`, `{"a": 1}`},
		{"with text", `blah {"a": 1} blah`, `{"a": 1}`},
		{"nested", `{"a": {"b": 2}}`, `{"a": {"b": 2}}`},
		{"with string braces", `{"a": "x{y}z"}`, `{"a": "x{y}z"}`},
		{"no json", "no json here", ""},
		{"escaped quotes", `{"a": "say \"hello\""}`, `{"a": "say \"hello\""}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ExtractJSONObject(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}
