package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// JSON-RPC 2.0 message types for MCP protocol.
// Following D3 (Parse Don't Validate): all messages are parsed into strong types
// at the transport boundary. Internal code never deals with raw JSON.

const jsonrpcVersion = "2.0"

// --- Errors ---

var (
	ErrInvalidJSON    = errors.New("invalid JSON")
	ErrInvalidJSONRPC = errors.New("invalid JSON-RPC version")
	ErrMissingMethod  = errors.New("method is required")
	ErrMissingID      = errors.New("id is required for request")
)

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// --- Request ---

// Request is a parsed JSON-RPC 2.0 request.
// Use ParseRequest to construct from raw bytes.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// MarshalJSON serializes a Request with the jsonrpc version field.
func (r Request) MarshalJSON() ([]byte, error) {
	type wire struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	return json.Marshal(wire{
		JSONRPC: jsonrpcVersion,
		ID:      r.ID,
		Method:  r.Method,
		Params:  r.Params,
	})
}

// ParseRequest parses raw JSON bytes into a strongly-typed Request.
// Returns an error if the message is not a valid JSON-RPC 2.0 request.
func ParseRequest(raw []byte) (Request, error) {
	var wire struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *int64          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if wire.JSONRPC != jsonrpcVersion {
		return Request{}, fmt.Errorf("%w: got %q", ErrInvalidJSONRPC, wire.JSONRPC)
	}
	if wire.ID == nil {
		return Request{}, ErrMissingID
	}
	if wire.Method == "" {
		return Request{}, ErrMissingMethod
	}
	return Request{
		ID:     *wire.ID,
		Method: wire.Method,
		Params: wire.Params,
	}, nil
}

// --- Response ---

// Response is a parsed JSON-RPC 2.0 response.
// Exactly one of Result or Error is set.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ResponseError  `json:"error,omitempty"`
}

// ResponseError is the error object in a JSON-RPC 2.0 response.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// MarshalJSON serializes a Response with the jsonrpc version field.
func (r Response) MarshalJSON() ([]byte, error) {
	type wire struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *ResponseError  `json:"error,omitempty"`
	}
	return json.Marshal(wire{
		JSONRPC: jsonrpcVersion,
		ID:      r.ID,
		Result:  r.Result,
		Error:   r.Error,
	})
}

// ParseResponse parses raw JSON bytes into a strongly-typed Response.
func ParseResponse(raw []byte) (Response, error) {
	var wire struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *ResponseError  `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if wire.JSONRPC != jsonrpcVersion {
		return Response{}, fmt.Errorf("%w: got %q", ErrInvalidJSONRPC, wire.JSONRPC)
	}
	return Response{
		ID:     wire.ID,
		Result: wire.Result,
		Error:  wire.Error,
	}, nil
}

// --- Notification ---

// Notification is a JSON-RPC 2.0 notification (no id, no response expected).
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// MarshalJSON serializes a Notification with the jsonrpc version field.
func (n Notification) MarshalJSON() ([]byte, error) {
	type wire struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	return json.Marshal(wire{
		JSONRPC: jsonrpcVersion,
		Method:  n.Method,
		Params:  n.Params,
	})
}

// --- Helpers ---

// NewRequest creates a Request with JSON-encoded params.
func NewRequest(id int64, method string, params interface{}) (Request, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Request{}, fmt.Errorf("marshal params: %w", err)
		}
		raw = b
	}
	return Request{ID: id, Method: method, Params: raw}, nil
}

// NewErrorResponse creates an error Response.
func NewErrorResponse(id int64, code int, message string) Response {
	return Response{
		ID:    id,
		Error: &ResponseError{Code: code, Message: message},
	}
}
