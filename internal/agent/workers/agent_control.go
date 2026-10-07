package workers

import (
	"context"
	"fmt"
)

// Agent control tools: the two ways a run can involve a human mid-flight.
//
// They are deliberately different operations, not one with a flag:
//
//	agent.pause  ends this run and waits. The run is resumable from exactly
//	             where it stopped, and nothing further happens until someone
//	             releases it.
//	agent.query  asks one question and CONTINUES with the answer. The run does
//	             not end, the worker stays busy for the duration of the
//	             question, and the model gets the answer as an observation.
//
// Collapsing them would lose that distinction at the call site, which is the
// same reason context.* is three tools rather than one.

// AgentPauseName is the pause tool's name.
const AgentPauseName = "agent.pause"

// AgentQueryName is the query tool's name.
const AgentQueryName = "agent.query"

// AgentPauseDef marks a run as waiting for a human decision.
func AgentPauseDef() *ToolDef {
	return &ToolDef{
		Name:        AgentPauseName,
		DisplayName: "Pause For Human",
		Category:    "agent",
		Description: "Stop this run and wait for a human decision. Use it when the next step needs a judgement you " +
			"cannot make: an irreversible action whose consequences are unclear, a choice between approaches with " +
			"different tradeoffs, or information only a person has. The run is saved and can be resumed from this " +
			"exact point, so pausing is not a failure and nothing is lost. Do NOT use it to ask a question you can " +
			"answer yourself — use agent.query for that, or an ordinary tool call.",
		InputSchema: map[string]ParamDef{
			"reason": {Type: "string",
				Description: "What decision is needed and from whom. Be specific: the person releasing this run reads only this.",
				Required:    true},
		},
		OutputSchema: map[string]ParamDef{
			"status": {Type: "string", Description: `"paused"`},
		},
		RequiredParams: []string{"reason"},
		Effect:         EffectRead,
		EstimatedTime:  0,
	}
}

// AgentQueryDef asks a human one question and continues with the answer.
func AgentQueryDef() *ToolDef {
	return &ToolDef{
		Name:        AgentQueryName,
		DisplayName: "Query Human",
		Category:    "agent",
		Description: "Ask a human one question and continue with their answer. Use it when you are blocked on a " +
			"detail only a person can supply (a preference, a credential, a decision between two acceptable " +
			"options) but the run itself should carry on afterwards. For a decision that needs the run to STOP " +
			"until someone acts, use agent.pause instead.",
		InputSchema: map[string]ParamDef{
			"question": {Type: "string", Description: "The question to ask. One question, answerable in a sentence.", Required: true},
			"options":  {Type: "array", Description: "Optional list of acceptable answers to offer the human"},
		},
		OutputSchema: map[string]ParamDef{
			"answer": {Type: "string", Description: "The human's answer"},
		},
		RequiredParams: []string{"question"},
		Effect:         EffectRead,
		EstimatedTime:  0,
	}
}

// agentControlUnavailable is the registry handler for both control tools.
//
// Like the context tools, these are intercepted by the loop before dispatch:
// pausing is a property of the run, not something a handler can perform, and a
// handler that silently did nothing would let the run continue as if a human
// had been consulted. Reaching this handler therefore means the interception is
// missing, and that has to be loud.
func agentControlUnavailable(name string) HandlerFunc {
	return func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return nil, fmt.Errorf(
			"%s must be handled by the agent loop, not dispatched as a handler; "+
				"reaching this handler means the interception is missing", name)
	}
}
