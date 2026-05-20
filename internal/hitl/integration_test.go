package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- OpenClaw Callback Tests ---

type mockHTTPClient struct {
	doFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.doFunc(req)
}

func TestOpenClawCallback_Notify(t *testing.T) {
	var captured []byte
	client := &mockHTTPClient{
		doFunc: func(req *http.Request) (*http.Response, error) {
			captured, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		},
	}

	cb := NewOpenClawCallback(OpenClawConfig{
		BaseURL:    "http://localhost:3000",
		SessionKey: "sess_1",
		Channel:    "feishu",
	}, client)

	req := &Request{
		ID:         "req_001",
		WorkflowID: "wf_abc",
		TaskID:     "review",
		Message:    "请审批 MR #42",
		Options:    []string{"approve", "reject"},
		TimeoutAt:  time.Now().Add(1 * time.Hour),
	}

	err := cb.Notify(context.Background(), req)
	if err != nil {
		t.Fatalf("notify: %v", err)
	}

	// Verify payload
	var msg openclawMessage
	if err := json.Unmarshal(captured, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.SessionKey != "sess_1" {
		t.Errorf("session_key: %s", msg.SessionKey)
	}
	if !strings.Contains(msg.Message, "req_001") {
		t.Error("message should contain request ID")
	}
	if !strings.Contains(msg.Message, "请审批 MR #42") {
		t.Error("message should contain request message")
	}
}

func TestOpenClawCallback_HTTPError(t *testing.T) {
	client := &mockHTTPClient{
		doFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader(`internal error`)),
			}, nil
		},
	}

	cb := NewOpenClawCallback(OpenClawConfig{BaseURL: "http://localhost:3000"}, client)

	err := cb.Notify(context.Background(), &Request{
		ID:         "req_002",
		WorkflowID: "wf_x",
		Message:    "test",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP error, got: %v", err)
	}
}

// --- Message Formatter Tests ---

func TestFormatRequest(t *testing.T) {
	f := NewMessageFormatter()
	req := &Request{
		ID:         "req_100",
		WorkflowID: "wf_bugfix",
		TaskID:     "approve_mr",
		Message:    "MR !5 需要审批",
		Options:    []string{"approve", "reject", "modify"},
		TimeoutAt:  time.Now().Add(2 * time.Hour),
	}

	msg := f.FormatRequest(req)
	if !strings.Contains(msg, "req_100") {
		t.Error("should contain request ID")
	}
	if !strings.Contains(msg, "wf_bugfix") {
		t.Error("should contain workflow ID")
	}
	if !strings.Contains(msg, "approve") {
		t.Error("should contain options")
	}
	if !strings.Contains(msg, "forge respond") {
		t.Error("should contain response instructions")
	}
}

func TestFormatNotification(t *testing.T) {
	f := NewMessageFormatter()
	req := &Request{
		ID:         "n_001",
		WorkflowID: "wf_1",
		Message:    "Bug 已自动修复，MR 已创建",
	}

	msg := f.FormatNotification(req)
	if !strings.Contains(msg, "Bug 已自动修复") {
		t.Error("should contain message")
	}
	if !strings.Contains(msg, "通知") {
		t.Error("should indicate notification type")
	}
}

func TestFormatTimeout_ZeroTime(t *testing.T) {
	f := NewMessageFormatter()
	s := f.formatTimeout(time.Time{})
	if s != "无限期" {
		t.Errorf("expected 无限期, got %s", s)
	}
}

// --- Handler Tests ---

func TestHandleRespond_Success(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.Create(context.Background(), &Request{
		ID:         "req_resp_1",
		WorkflowID: "wf_1",
		Message:    "approve?",
		Options:    []string{"approve", "reject"},
	})

	handler := NewHandler(mgr, nil)
	body := `{"request_id":"req_resp_1","decision":"approve","feedback":"LGTM"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	handler.HandleRespond(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["decision"] != "approve" {
		t.Errorf("decision: %s", resp["decision"])
	}
}

func TestHandleRespond_NotFound(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	handler := NewHandler(mgr, nil)

	body := `{"request_id":"nonexistent","decision":"approve"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	handler.HandleRespond(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleRespond_MissingFields(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	handler := NewHandler(mgr, nil)

	// Missing decision
	body := `{"request_id":"req_1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.HandleRespond(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleList(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.Create(context.Background(), &Request{ID: "p1", WorkflowID: "wf_1", Message: "m1"})
	mgr.Create(context.Background(), &Request{ID: "p2", WorkflowID: "wf_2", Message: "m2"})

	handler := NewHandler(mgr, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/hitl/pending", nil)
	w := httptest.NewRecorder()

	handler.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("expected 2 pending, got %d", count)
	}
}

// --- ParseCommand Tests ---

func TestParseCommand_Full(t *testing.T) {
	id, decision, feedback, err := ParseCommand("forge respond req_001 approve looks good")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != "req_001" || decision != "approve" || feedback != "looks good" {
		t.Errorf("got: id=%s decision=%s feedback=%s", id, decision, feedback)
	}
}

func TestParseCommand_NoFeedback(t *testing.T) {
	id, decision, feedback, err := ParseCommand("forge respond req_002 reject")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != "req_002" || decision != "reject" || feedback != "" {
		t.Errorf("got: id=%s decision=%s feedback=%s", id, decision, feedback)
	}
}

func TestParseCommand_TooShort(t *testing.T) {
	_, _, _, err := ParseCommand("forge respond req_only")
	if err == nil {
		t.Error("expected error for too few arguments")
	}
}
