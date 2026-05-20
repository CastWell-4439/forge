package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Handler provides HTTP endpoints for HITL interactions.
type Handler struct {
	manager  *Manager
	callback *OpenClawCallback
}

// NewHandler creates a HITL HTTP handler.
func NewHandler(manager *Manager, callback *OpenClawCallback) *Handler {
	return &Handler{
		manager:  manager,
		callback: callback,
	}
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

	if err := h.manager.Respond(r.Context(), req.RequestID, resp); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
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
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":     "ok",
		"request_id": req.RequestID,
		"decision":   req.Decision,
	})
}

// HandleList handles GET /api/hitl/pending — lists pending HITL requests.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.manager.mu.RLock()
	reqs := make([]*Request, 0, len(h.manager.pending))
	for _, req := range h.manager.pending {
		reqs = append(reqs, req)
	}
	h.manager.mu.RUnlock()

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
