package structured

// AgentResponse is the structured output format for the Agent's ReAct loop.
// The LLM must return exactly one of: Answer (final response) or Action (tool call).
// Thought is always present — it's the agent's reasoning trace.
type AgentResponse struct {
	// Thought is the agent's chain-of-thought reasoning for this step.
	// Always present.
	Thought string `json:"thought" desc:"Your step-by-step reasoning about what to do next"`

	// Action is set when the agent wants to call a tool.
	// Mutually exclusive with Answer.
	Action *ToolCallRequest `json:"action,omitempty" desc:"Tool to invoke (omit if providing final answer)"`

	// Answer is set when the agent has a final response for the user.
	// Mutually exclusive with Action.
	Answer string `json:"answer,omitempty" desc:"Final answer to the user (omit if invoking a tool)"`
}

// ToolCallRequest describes a tool the agent wants to invoke.
type ToolCallRequest struct {
	// Name is the tool/handler name (e.g. "video.probe", "media.download").
	Name string `json:"name" desc:"Tool name to call"`

	// Params is the parameters to pass to the tool, as a JSON object.
	Params map[string]interface{} `json:"params" desc:"Tool parameters as key-value pairs"`
}

// IsTerminal returns true if this response contains a final answer.
func (r *AgentResponse) IsTerminal() bool {
	return r.Answer != ""
}

// IsToolCall returns true if this response requests a tool invocation.
func (r *AgentResponse) IsToolCall() bool {
	return r.Action != nil && r.Action.Name != ""
}

// Validate checks that the response is well-formed:
// - Thought must be non-empty
// - Exactly one of Action or Answer must be set
func (r *AgentResponse) Validate() error {
	if r.Thought == "" {
		return &ValidationError{Field: "thought", Message: "thought is required"}
	}
	hasAction := r.Action != nil && r.Action.Name != ""
	hasAnswer := r.Answer != ""

	if hasAction && hasAnswer {
		return &ValidationError{Field: "action/answer", Message: "cannot have both action and answer"}
	}
	if !hasAction && !hasAnswer {
		return &ValidationError{Field: "action/answer", Message: "must have either action or answer"}
	}
	return nil
}

// ValidationError represents a structured output validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "structured output validation: " + e.Field + ": " + e.Message
}
