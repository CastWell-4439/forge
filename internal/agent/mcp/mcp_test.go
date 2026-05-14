package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/castwell/forge/internal/agent/core"
)

// --- JSON-RPC Tests ---

func TestParseRequest_Valid(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := ParseRequest(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ID != 1 {
		t.Errorf("id = %d, want 1", req.ID)
	}
	if req.Method != "tools/list" {
		t.Errorf("method = %q, want %q", req.Method, "tools/list")
	}
}

func TestParseRequest_WithParams(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"test"}}`)
	req, err := ParseRequest(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ID != 42 {
		t.Errorf("id = %d, want 42", req.ID)
	}
	if req.Params == nil {
		t.Error("params should not be nil")
	}
}

func TestParseRequest_MissingMethod(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1}`)
	_, err := ParseRequest(raw)
	if err == nil {
		t.Fatal("expected error for missing method")
	}
}

func TestParseRequest_MissingID(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","method":"test"}`)
	_, err := ParseRequest(raw)
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestParseRequest_WrongVersion(t *testing.T) {
	raw := []byte(`{"jsonrpc":"1.0","id":1,"method":"test"}`)
	_, err := ParseRequest(raw)
	if err == nil {
		t.Fatal("expected error for wrong version")
	}
}

func TestParseRequest_InvalidJSON(t *testing.T) {
	raw := []byte(`not json`)
	_, err := ParseRequest(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseResponse_Success(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("id = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Error("error should be nil for success response")
	}
	if resp.Result == nil {
		t.Error("result should not be nil")
	}
}

func TestParseResponse_Error(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("error should not be nil")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
	if resp.Error.Message != "Method not found" {
		t.Errorf("message = %q", resp.Error.Message)
	}
}

func TestNewRequest_MarshalRoundtrip(t *testing.T) {
	params := map[string]string{"name": "test_tool"}
	req, err := NewRequest(7, "tools/call", params)
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	parsed, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if parsed.ID != 7 {
		t.Errorf("id = %d, want 7", parsed.ID)
	}
	if parsed.Method != "tools/call" {
		t.Errorf("method = %q", parsed.Method)
	}
}

func TestNewErrorResponse_Marshal(t *testing.T) {
	resp := NewErrorResponse(5, CodeInternalError, "something broke")
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	parsed, err := ParseResponse(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if parsed.ID != 5 {
		t.Errorf("id = %d, want 5", parsed.ID)
	}
	if parsed.Error == nil {
		t.Fatal("expected error")
	}
	if parsed.Error.Code != CodeInternalError {
		t.Errorf("code = %d", parsed.Error.Code)
	}
}

func TestNotification_Marshal(t *testing.T) {
	n := Notification{Method: "notifications/initialized"}
	data, err := n.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if m["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", m["jsonrpc"])
	}
	if m["method"] != "notifications/initialized" {
		t.Errorf("method = %v", m["method"])
	}
	if _, hasID := m["id"]; hasID {
		t.Error("notification should not have id")
	}
}

// --- Mock Transport ---

type mockTransport struct {
	responses map[string]json.RawMessage
	closed    bool
}

func newMockTransport() *mockTransport {
	return &mockTransport{responses: make(map[string]json.RawMessage)}
}

func (m *mockTransport) addResponse(method string, result interface{}) {
	data, _ := json.Marshal(result)
	m.responses[method] = data
}

func (m *mockTransport) Send(_ context.Context, req Request) (Response, error) {
	result, ok := m.responses[req.Method]
	if !ok {
		return NewErrorResponse(req.ID, CodeMethodNotFound, "not found"), nil
	}
	return Response{ID: req.ID, Result: result}, nil
}

func (m *mockTransport) Notify(_ context.Context, _ Notification) error { return nil }

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// --- Client Tests ---

func TestClient_Initialize(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	if client.ServerName() != "test-server" {
		t.Errorf("server name = %q, want %q", client.ServerName(), "test-server")
	}
}

func TestClient_ListTools(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{Name: "file.read", Description: "Read a file"},
			{Name: "web.search", Description: "Search the web"},
		},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "file.read" {
		t.Errorf("tool[0].Name = %q", tools[0].Name)
	}
}

func TestClient_CallTool_Success(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})
	mt.addResponse("tools/call", toolCallResult{
		Content: []toolContent{{Type: "text", Text: "hello world"}},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	output, err := client.CallTool(context.Background(), "test_tool", nil)
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if output != "hello world" {
		t.Errorf("output = %q, want %q", output, "hello world")
	}
}

func TestClient_CallTool_Error(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})
	mt.addResponse("tools/call", toolCallResult{
		Content: []toolContent{{Type: "text", Text: "file not found"}},
		IsError: true,
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	_, err = client.CallTool(context.Background(), "bad_tool", nil)
	if err == nil {
		t.Fatal("expected error for tool error response")
	}
}

func TestClient_Close(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if !mt.closed {
		t.Error("transport should be closed")
	}
}

// --- Manager Tests ---

func TestManager_CallTool_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	result, err := mgr.CallTool(context.Background(), "nonexistent", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error in result for unknown tool")
	}
}

func TestManager_CallTool_Routes(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})
	mt.addResponse("tools/call", toolCallResult{
		Content: []toolContent{{Type: "text", Text: "routed result"}},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	mgr := NewManager(nil)
	mgr.clients["server-a"] = client
	mgr.tools["my.tool"] = "server-a"

	result, err := mgr.CallTool(context.Background(), "my.tool", nil)
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if result.Output != "routed result" {
		t.Errorf("output = %q, want %q", result.Output, "routed result")
	}
}

func TestManager_Stop(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "s", Version: "1.0"},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	mgr := NewManager(nil)
	mgr.clients["s"] = client
	mgr.tools["t"] = "s"

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if len(mgr.clients) != 0 {
		t.Error("clients should be empty after Stop")
	}
	if len(mgr.tools) != 0 {
		t.Error("tools should be empty after Stop")
	}
}

// --- Bridge Tests ---

func TestBridge_Sync(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	mgr := NewManager(nil)
	mgr.clients["test"] = client
	mgr.tools["mcp.file_read"] = "test"
	mgr.tools["mcp.web_search"] = "test"

	registry := core.NewToolRegistry()
	bridge := NewBridge(mgr, registry)

	count, err := bridge.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if count != 2 {
		t.Errorf("registered %d tools, want 2", count)
	}
	if !registry.HasHandler("mcp.file_read") {
		t.Error("mcp.file_read not registered")
	}
	if !registry.HasHandler("mcp.web_search") {
		t.Error("mcp.web_search not registered")
	}
}

func TestBridge_Sync_NativeToolTakesPrecedence(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
	})

	client, err := NewClient(context.Background(), mt)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	mgr := NewManager(nil)
	mgr.clients["test"] = client
	mgr.tools["native_tool"] = "test"

	registry := core.NewToolRegistry()
	// Pre-register a native tool — should NOT be overwritten.
	nativeDef := &core.ToolDef{Name: "native_tool", Description: "native"}
	_ = registry.Register(nativeDef, func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"native": true}, nil
	})

	bridge := NewBridge(mgr, registry)

	count, err := bridge.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if count != 0 {
		t.Errorf("registered %d tools, want 0 (native takes precedence)", count)
	}
}
