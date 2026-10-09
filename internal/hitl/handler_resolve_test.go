package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postRespond drives the respond endpoint and returns the recorder plus the
// decoded body.
func postRespond(t *testing.T, h *Handler, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleRespond(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec, decoded
}

// A recorded decision must reach the waiting task. Recording it and stopping
// there is indistinguishable, from the reviewer's side, from nothing happening:
// the task stays parked and the request looks answered.
func TestRespondAppliesTheDecision(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	ctx := context.Background()
	require.NoError(t, mgr.Create(ctx, &Request{
		ID: "req-apply", WorkflowID: "wf-1", TaskID: "task-1",
		Message: "Proceed?", Options: []string{"approve", "reject"},
	}))

	h := NewHandler(mgr, nil)

	var gotReq *Request
	var gotResp *Response
	h.SetResolveFunc(func(_ context.Context, req *Request, resp *Response) error {
		gotReq = req
		gotResp = resp
		return nil
	})

	rec, body := postRespond(t, h, `{"request_id":"req-apply","decision":"approve","feedback":"LGTM"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", body["status"])
	require.NotNil(t, gotReq, "the resolver must be called: that is what releases the task")
	assert.Equal(t, "task-1", gotReq.TaskID, "and it must carry the task id")
	require.NotNil(t, gotResp)
	assert.Equal(t, "approve", gotResp.Decision)
	assert.Equal(t, "LGTM", gotResp.Feedback)
}

// A rejection must be applied too — it is the other half of the decision, and a
// pipeline that only resumes on approve would leave rejected tasks parked.
func TestRespondAppliesRejection(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-reject", WorkflowID: "wf-1", TaskID: "task-2",
		Message: "Proceed?", Options: []string{"approve", "reject"},
	}))

	h := NewHandler(mgr, nil)
	var decision string
	h.SetResolveFunc(func(_ context.Context, _ *Request, resp *Response) error {
		decision = resp.Decision
		return nil
	})

	rec, _ := postRespond(t, h, `{"request_id":"req-reject","decision":"reject"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "reject", decision)
}

// A decision that could not be applied is reported as such. Answering "ok" while
// the task stays parked would tell the reviewer their approval took effect when
// it did not — and they would never look again.
func TestRespondReportsWhenTheDecisionCouldNotBeApplied(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-fail", WorkflowID: "wf-1", TaskID: "task-3",
		Message: "Proceed?", Options: []string{"approve"},
	}))

	h := NewHandler(mgr, nil)
	h.SetResolveFunc(func(_ context.Context, _ *Request, _ *Response) error {
		return errors.New("task is not PAUSED")
	})

	rec, body := postRespond(t, h, `{"request_id":"req-fail","decision":"approve"}`)

	assert.Equal(t, http.StatusAccepted, rec.Code,
		"the decision was recorded but not applied, which is not plain success")
	assert.Equal(t, "recorded_not_applied", body["status"])
	assert.Contains(t, body["warning"], "not PAUSED")
}

// With no resolver installed the endpoint says so rather than implying the task
// resumed. This is the honest-degradation case: a deployment that wires the
// endpoint but not the release path.
func TestRespondSaysWhenNoResolverIsConfigured(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-noresolver", WorkflowID: "wf-1", TaskID: "task-4",
		Message: "Proceed?", Options: []string{"approve"},
	}))

	h := NewHandler(mgr, nil) // no SetResolveFunc

	rec, body := postRespond(t, h, `{"request_id":"req-noresolver","decision":"approve"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "recorded_not_applied", body["status"])
	assert.Contains(t, body["warning"], "no resolver")
}

// A request that is not pending is refused, and the resolver is not called: the
// decision has nowhere to go, and calling it would release a task twice.
func TestRespondRefusesAnAlreadyResolvedRequest(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	ctx := context.Background()
	require.NoError(t, mgr.Create(ctx, &Request{
		ID: "req-twice", WorkflowID: "wf-1", TaskID: "task-5",
		Message: "Proceed?", Options: []string{"approve"},
	}))

	h := NewHandler(mgr, nil)
	calls := 0
	h.SetResolveFunc(func(_ context.Context, _ *Request, _ *Response) error {
		calls++
		return nil
	})

	rec, _ := postRespond(t, h, `{"request_id":"req-twice","decision":"approve"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, calls)

	rec2, _ := postRespond(t, h, `{"request_id":"req-twice","decision":"approve"}`)
	assert.Equal(t, http.StatusNotFound, rec2.Code,
		"answering the same request twice must be refused")
	assert.Equal(t, 1, calls, "and must not release the task a second time")
}

// The endpoint still validates its input before touching anything.
func TestRespondValidatesInput(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	h := NewHandler(mgr, nil)
	h.SetResolveFunc(func(_ context.Context, _ *Request, _ *Response) error {
		t.Fatal("the resolver must not run for a malformed request")
		return nil
	})

	rec, _ := postRespond(t, h, `{"decision":"approve"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a missing request_id is a client error")

	rec2, _ := postRespond(t, h, `{"request_id":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec2.Code, "a missing decision is a client error")
}
