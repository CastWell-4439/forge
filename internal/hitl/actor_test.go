package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/identity"
)

// The gap this closes: a HITL approval used to record the decision with no
// subject, so "who approved this release" had no answer — and the whole point of
// a human-in-the-loop gate is that a human is answerable for it.
func TestResponseRecordsWhoDecided(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	ctx := context.Background()

	req := &Request{
		ID:         "req-1",
		WorkflowID: "wf-1",
		TaskID:     "t-1",
		Message:    "approve the release?",
		Options:    []string{"approve", "reject"},
	}
	require.NoError(t, mgr.Create(ctx, req))

	actor := identity.NewDeclared("alice")
	require.NoError(t, mgr.Respond(ctx, "req-1", &Response{
		Decision: "approve",
		Feedback: "looks good",
		Actor:    actor,
	}))

	got, err := mgr.Get(ctx, "req-1")
	require.NoError(t, err)
	require.NotNil(t, got.Response)

	assert.Equal(t, "approve", got.Response.Decision)
	assert.Equal(t, "alice", got.Response.Actor.ID())
	assert.False(t, got.Response.Actor.Verified(),
		"the declaration is unverified, and the record says so")
}

// A response with no actor records that nobody was declared — distinct from a
// response that names someone.
func TestResponseWithoutAnActorIsUnattributed(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	ctx := context.Background()

	require.NoError(t, mgr.Create(ctx, &Request{
		ID: "req-2", WorkflowID: "wf-1", TaskID: "t-1",
		Message: "?", Options: []string{"approve"},
	}))
	require.NoError(t, mgr.Respond(ctx, "req-2", &Response{Decision: "approve"}))

	got, err := mgr.Get(ctx, "req-2")
	require.NoError(t, err)

	_, declared := got.Response.Actor.Declared()
	assert.False(t, declared)
	assert.Equal(t, "unattributed", got.Response.Actor.String())
}

// The HTTP handler reads the caller's declaration from the header, which is the
// path a real operator takes.
func TestHTTPHandlerRecordsTheDeclaredActor(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-3", WorkflowID: "wf-1", TaskID: "t-1",
		Message: "approve?", Options: []string{"approve", "reject"},
	}))

	handler := NewHandler(mgr, nil)

	body, err := json.Marshal(map[string]string{
		"request_id": "req-3",
		"decision":   "approve",
		"feedback":   "ship it",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", bytes.NewReader(body))
	req.Header.Set(identity.HTTPHeader, "bob")
	rec := httptest.NewRecorder()

	handler.HandleRespond(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	got, err := mgr.Get(context.Background(), "req-3")
	require.NoError(t, err)
	require.NotNil(t, got.Response)
	assert.Equal(t, "bob", got.Response.Actor.ID())
	assert.Equal(t, "approve", got.Response.Decision)
}

// A request with no declaration header still works: attribution is recorded as
// absent, not required. Refusing unidentified callers would break every existing
// client over a field that is not verified anyway.
func TestHTTPHandlerWithoutAnActorHeaderStillWorks(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-4", WorkflowID: "wf-1", TaskID: "t-1",
		Message: "approve?", Options: []string{"approve"},
	}))

	handler := NewHandler(mgr, nil)
	body, _ := json.Marshal(map[string]string{"request_id": "req-4", "decision": "approve"})

	req := httptest.NewRequest(http.MethodPost, "/api/hitl/respond", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleRespond(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	got, err := mgr.Get(context.Background(), "req-4")
	require.NoError(t, err)
	_, declared := got.Response.Actor.Declared()
	assert.False(t, declared, "no header means nobody was declared, not a failure")
}

// The in-process path reads the actor from the context, so a coordinator handler
// that already has one does not have to pass it separately.
func TestWorkflowAPITakesTheActorFromContext(t *testing.T) {
	mgr := NewManager(ManagerConfig{Store: newMemStore(), Timeout: time.Hour})
	require.NoError(t, mgr.Create(context.Background(), &Request{
		ID: "req-5", WorkflowID: "wf-1", TaskID: "t-1",
		Message: "?", Options: []string{"approve"},
	}))

	api := NewWorkflowAPI(WorkflowAPIConfig{HITLManager: mgr})
	ctx := identity.WithActor(context.Background(), identity.NewDeclared("carol"))

	require.NoError(t, api.ResumeHITL(ctx, "req-5", "approve", ""))

	got, err := mgr.Get(context.Background(), "req-5")
	require.NoError(t, err)
	assert.Equal(t, "carol", got.Response.Actor.ID())
}
