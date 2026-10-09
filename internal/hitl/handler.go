package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// ResolveFunc acts on a decision about a request: it is how the answer reaches
// the task that is waiting on it.
//
// It is a function rather than a coordinator reference because this package must
// not depend on the coordinator — the HITL manager already runs inside the
// worker, and importing the scheduler from there would make the dependency
// circular in spirit even where Go allows it. The assembly layer knows both
// sides and supplies the bridge.
//
// A decision that is not acted on leaves the task parked forever with a human
// believing they answered it, so a non-nil ResolveFunc is what makes "responded"
// mean anything.
type ResolveFunc func(ctx context.Context, req *Request, resp *Response) error

// Handler provides HTTP endpoints for HITL interactions.
type Handler struct {
	manager  *Manager
	callback *OpenClawCallback
	resolve  ResolveFunc
}

// NewHandler creates a HITL HTTP handler.
func NewHandler(manager *Manager, callback *OpenClawCallback) *Handler {
	return &Handler{
		manager:  manager,
		callback: callback,
	}
}

// SetResolveFunc installs the hook that releases the task behind a request.
// Without it the endpoint still records the decision, but nothing resumes.
func (h *Handler) SetResolveFunc(fn ResolveFunc) {
	h.resolve = fn
}

// RespondRequest is the JSON body for responding to a HITL request.
type RespondRequest struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Feedback  string `json:"feedback,omitempty"`
}

// HandleRespond handles POST /api/hitl/respond.
func (h *Handler) HandleRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RespondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.RequestID == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}
	if req.Decision == "" {
		http.Error(w, "decision is required", http.StatusBadRequest)
		return
	}

	resp := &Response{
		Decision: req.Decision,
		Feedback: req.Feedback,
	}

	// Look the request up BEFORE recording the answer: once Respond marks it
	// responded it leaves the pending set, and the task id needed to release the
	// waiting task would have to be re-read from the store.
	var request *Request
	if h.manager != nil {
		request, _ = h.manager.Get(r.Context(), req.RequestID)
	}

	if err := h.manager.Respond(r.Context(), req.RequestID, resp); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Release the task the request was blocking. This is the half that makes the
	// endpoint worth having: without it the decision is recorded and the task
	// stays parked, which is indistinguishable from nobody having answered.
	var resolveErr error
	if h.resolve != nil && request != nil {
		resolveErr = h.resolve(r.Context(), request, resp)
		if resolveErr != nil {
			log.Printf("ERROR: hitl: apply decision for request %s: %v", req.RequestID, resolveErr)
		}
	}

	// Send confirmation back to user via OpenClaw
	if h.callback != nil {
		hitlReq, _ := h.manager.Get(r.Context(), req.RequestID)
		if hitlReq == nil {
			// Request was responded to (removed from pending), recreate minimal for confirmation
			hitlReq = &Request{ID: req.RequestID}
		}
		confirmMsg := h.callback.formatter.FormatResponseConfirmation(hitlReq, resp)
		h.callback.Notify(context.Background(), &Request{
			ID:         "confirm_" + req.RequestID,
			WorkflowID: "system",
			Message:    confirmMsg,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	// A decision that could not be applied is reported as such. Answering "ok"
	// while the waiting task stays parked would tell the reviewer their approval
	// took effect when it did not.
	body := map[string]any{
		"status":     "ok",
		"request_id": req.RequestID,
		"decision":   req.Decision,
	}
	if resolveErr != nil {
		body["status"] = "recorded_not_applied"
		body["warning"] = resolveErr.Error()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(body)
		return
	}
	if h.resolve == nil {
		// No hook installed: the decision is durable, but nothing was resumed.
		body["warning"] = "no resolver configured; the decision was recorded but no task was resumed"
		body["status"] = "recorded_not_applied"
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(body)
}

// HandleList handles GET /api/hitl/pending — lists pending HITL requests.
//
// It lists through the manager, which merges its own memory with the store.
// Listing only memory was wrong for the shape this runs in: the worker files the
// requests and the coordinator serves this endpoint, so an operator would have
// seen an empty queue while approvals were actually waiting.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reqs, err := h.manager.ListPending(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"count":    len(reqs),
		"requests": reqs,
	})
}

// RegisterRoutes registers HITL HTTP routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/hitl/respond", h.HandleRespond)
	mux.HandleFunc("/api/hitl/pending", h.HandleList)
}

// ParseCommand parses a text command like "forge respond <id> <decision> [feedback]".
// Returns (requestID, decision, feedback, error).
func ParseCommand(text string) (string, string, string, error) {
	text = strings.TrimSpace(text)

	// Strip "forge respond " prefix
	prefixes := []string{"forge respond ", "forge hitl respond "}
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(text), prefix) {
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}

	parts := strings.SplitN(text, " ", 3)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("usage: forge respond <request_id> <decision> [feedback]")
	}

	requestID := parts[0]
	decision := parts[1]
	feedback := ""
	if len(parts) > 2 {
		feedback = parts[2]
	}

	return requestID, decision, feedback, nil
}
