package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Server-to-client requests, the 2026-07-28 way (H4).
//
// The previous protocol revision let a server send the client a request
// mid-call (roots/list, sampling/createMessage, elicitation/create). That
// pattern is GONE: the modern specification says servers MUST express the same
// need as an InputRequiredResult — a normal response whose resultType is
// "input_required" and whose inputRequests field lists what is missing. The
// client gathers the answers and RETRIES the original request with them.
//
// Why this had to be implemented rather than deferred: without it, a compliant
// server's perfectly legal answer is parsed as an ordinary tool result. The
// content is empty, IsError is false, and the caller receives "" — a tool call
// that "succeeded" with no output. That is not a missing feature; it is a
// misread of a valid response, and the model would treat it as "the tool
// returned nothing" and carry on.
//
// What this does NOT do: implement sampling or roots. Both are deprecated in
// the modern specification (a client should integrate with its LLM provider
// directly rather than proxy the server's sampling request), and silently
// answering them would be worse than refusing — so a request we cannot fulfil
// is REFUSED, and the refusal is reported. A server that needs sampling gets a
// clear error instead of a fabricated answer.

// resultTypeInputRequired is the resultType that marks an interim response
// carrying server-to-client requests.
const resultTypeInputRequired = "input_required"

// inputRequest is one entry of an InputRequiredResult's inputRequests map.
type inputRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// inputRequiredResult is the shape a server uses to ask for more information.
type inputRequiredResult struct {
	ResultType    string                  `json:"resultType"`
	InputRequests map[string]inputRequest `json:"inputRequests,omitempty"`
	// RequestState is opaque and must be echoed back on the retry. The
	// specification is explicit that clients must not inspect or modify it.
	RequestState string `json:"requestState,omitempty"`
}

// InputHandler answers one server-to-client request.
//
// It returns the result value to send back, or an error when this client will
// not fulfil the request. Refusing is a first-class outcome: sampling and roots
// are deprecated, and pretending to answer them would fabricate data.
type InputHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// ErrInputUnsupported reports that this client does not implement a requested
// server-to-client method. It is a sentinel so callers can distinguish "we
// refuse by design" from "the handler failed".
var ErrInputUnsupported = fmt.Errorf("client does not support this server-to-client request")

// maxInputRetries bounds how many round trips one tool call may take. A server
// that keeps asking for more input would otherwise loop forever; the
// specification allows several rounds, so the limit is generous but finite.
const maxInputRetries = 4

// SetInputHandler installs the handler used to answer server-to-client
// requests. Without one, every such request is refused (see defaultInputHandler).
func (c *Client) SetInputHandler(h InputHandler) { c.inputHandler = h }

// inputHandlerOrDefault returns the installed handler or the refusing default.
func (c *Client) inputHandlerOrDefault() InputHandler {
	if c.inputHandler != nil {
		return c.inputHandler
	}
	return defaultInputHandler
}

// defaultInputHandler refuses every server-to-client request, naming the method.
//
// The refusal is deliberate and specific. `elicitation/create` is answerable in
// principle (it is a question), but this client has no interactive channel
// wired at this layer — the agent's ask.user is where that lives. `sampling/*`
// and `roots/list` are deprecated in the modern specification. In all cases the
// honest answer is "not supported", which the caller can act on, rather than a
// plausible-looking fabrication.
func defaultInputHandler(_ context.Context, method string, _ json.RawMessage) (any, error) {
	return nil, fmt.Errorf("%w: %s", ErrInputUnsupported, method)
}

// callToolWithInput runs tools/call, answering server-to-client requests until
// the server produces a final result.
//
// This is the retry loop the specification describes: send, receive
// input_required, gather answers, retry with inputResponses (and the opaque
// requestState echoed back), repeat. Only a non-input_required result ends it.
func (c *Client) callToolWithInput(ctx context.Context, name string, arguments json.RawMessage) (toolCallResult, error) {
	params := toolCallParams{Name: name, Arguments: arguments}
	// pending accumulates answers across rounds: a server may ask again, and
	// the earlier answers must keep travelling with the retry.
	pending := map[string]any{}
	// requestState is the server's opaque correlation token. It sits BESIDE
	// inputResponses on the retry, not inside it — putting it in the answers
	// map would send the server a request key it never issued.
	requestState := ""

	for attempt := 0; attempt <= maxInputRetries; attempt++ {
		var requestParams any = params
		if len(pending) > 0 || requestState != "" {
			requestParams = toolCallParamsWithInput{
				toolCallParams: params,
				InputResponses: pending,
				RequestState:   requestState,
			}
		}

		resp, err := c.call(ctx, "tools/call", requestParams)
		if err != nil {
			return toolCallResult{}, err
		}

		var interim inputRequiredResult
		if err := json.Unmarshal(resp.Result, &interim); err != nil {
			return toolCallResult{}, fmt.Errorf("parse tools/call result: %w", err)
		}
		if interim.ResultType != resultTypeInputRequired {
			// A final result. Decode it as one; the caller decides what to do
			// with IsError/content.
			var final toolCallResult
			if err := json.Unmarshal(resp.Result, &final); err != nil {
				return toolCallResult{}, fmt.Errorf("parse tools/call result: %w", err)
			}
			return final, nil
		}

		if len(interim.InputRequests) == 0 {
			// input_required with nothing to answer: there is no way to make
			// progress, and retrying identical bytes would loop. Say so.
			return toolCallResult{}, fmt.Errorf(
				"tool %q: server asked for input but listed no requests (requestState %q)",
				name, interim.RequestState)
		}

		answered, err := c.answerInputRequests(ctx, interim.InputRequests)
		if err != nil {
			return toolCallResult{}, fmt.Errorf("tool %q: %w", name, err)
		}
		for key, value := range answered {
			pending[key] = value
		}
		// requestState travels back untouched; it is the server's token for
		// correlating the retry, and the specification forbids inspecting it.
		requestState = interim.RequestState
	}

	return toolCallResult{}, fmt.Errorf(
		"tool %q: server requested more input %d times without completing", name, maxInputRetries)
}

// toolCallParamsWithInput is the retry body: the original call plus the answers
// and the opaque state, side by side as the specification lays them out.
type toolCallParamsWithInput struct {
	toolCallParams
	InputResponses map[string]any `json:"inputResponses,omitempty"`
	RequestState   string         `json:"requestState,omitempty"`
}

// answerInputRequests asks the installed handler for each request.
//
// The FIRST failure aborts the whole call: a partially answered set would be
// sent as if it were complete, and the server would act on missing input. Keys
// are processed in sorted order so behaviour is deterministic.
func (c *Client) answerInputRequests(ctx context.Context, requests map[string]inputRequest) (map[string]any, error) {
	handler := c.inputHandlerOrDefault()
	keys := make([]string, 0, len(requests))
	for key := range requests {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	answers := make(map[string]any, len(requests))
	for _, key := range keys {
		req := requests[key]
		answer, err := handler(ctx, req.Method, req.Params)
		if err != nil {
			return nil, fmt.Errorf("cannot answer server request %q (%s): %w", key, req.Method, err)
		}
		answers[key] = answer
	}
	return answers, nil
}

// isInputRequired reports whether a raw result carries the interim marker. Used
// by the tests and by any caller that needs to inspect a result it decoded
// itself.
func isInputRequired(raw json.RawMessage) bool {
	var probe struct {
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.TrimSpace(probe.ResultType) == resultTypeInputRequired
}
