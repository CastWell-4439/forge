// Package productapi exposes the local ForgeX Control Plane product API.
package productapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// Config controls the local product API server.
type Config struct {
	Root    string
	Version string
}

// Server serves read-only product-level ForgeX APIs over local artifacts.
type Server struct {
	version string
	service *Service
}

// RunSummary is the product-level list projection for a run.
type RunSummary struct {
	ID         string          `json:"id"`
	TaskID     string          `json:"task_id"`
	Name       string          `json:"name"`
	Status     model.RunStatus `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    time.Time       `json:"ended_at,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Workspace  string          `json:"workspace"`
	Project    string          `json:"project,omitempty"`
	AssetCount int             `json:"asset_count"`
	ErrorCount int             `json:"error_count"`
}

// RunDetail is the product-level detail projection for one run.
type RunDetail struct {
	Run        model.Run        `json:"run"`
	TaskPacket model.TaskPacket `json:"task_packet"`
	Workspace  string           `json:"workspace"`
	Project    string           `json:"project,omitempty"`
	Metrics    RunMetrics       `json:"metrics"`
}

// RunMetrics contains high-level counts useful for product dashboards.
type RunMetrics struct {
	Events              int `json:"events"`
	ToolCalls           int `json:"tool_calls"`
	PolicyDecisions     int `json:"policy_decisions"`
	ContractValidations int `json:"contract_validations"`
	Errors              int `json:"errors"`
	Lessons             int `json:"lessons"`
	Artifacts           int `json:"artifacts"`
	StopDecisions       int `json:"stop_decisions"`
	GateDecisions       int `json:"gate_decisions"`
	HITLReviews         int `json:"hitl_reviews"`
}

// AssetSummary is a future-proof local asset registry projection.
type AssetSummary struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	RunID       string    `json:"run_id"`
	Project     string    `json:"project,omitempty"`
	Path        string    `json:"path"`
	ContentType string    `json:"content_type,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// New creates a local ForgeX product API server.
func New(cfg Config) *Server {
	version := cfg.Version
	if version == "" {
		version = "unknown"
	}
	return &Server{version: version, service: NewService(cfg.Root)}
}

// Handler returns the HTTP handler for the product API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/overview", s.handleOverview)
	mux.HandleFunc("GET /api/v1/workspaces", s.handleWorkspaces)
	mux.HandleFunc("GET /api/v1/projects", s.handleProjects)
	mux.HandleFunc("GET /api/v1/projects/", s.handleProjectSubresource)
	mux.HandleFunc("GET /api/v1/runs", s.handleRuns)
	mux.HandleFunc("GET /api/v1/runs/", s.handleRunSubresource)
	mux.HandleFunc("POST /api/v1/runs/", s.handleRunMutation)
	mux.HandleFunc("GET /api/v1/reliability/repeat-result", s.handleRepeatResult)
	mux.HandleFunc("GET /api/v1/assets", s.handleAssets)
	return recoverMiddleware(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "ForgeX Control Plane",
		"version": s.version,
		"mode":    "local",
		"root":    s.service.Root(),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.service.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := s.service.ListWorkspaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_workspaces_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.service.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_projects_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleProjectSubresource(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "project id is required")
		return
	}
	projectID := parts[0]
	if !safeID(projectID) {
		writeError(w, http.StatusBadRequest, "invalid_project_id", "project id contains invalid path characters")
		return
	}
	if len(parts) == 1 {
		project, err := s.service.GetProject(projectID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "project_not_found", fmt.Sprintf("project %s was not found", projectID))
				return
			}
			writeError(w, http.StatusInternalServerError, "read_project_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, project)
		return
	}
	if len(parts) == 2 && parts[1] == "runs" {
		runs, err := s.service.ListRunsByProject(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_project_runs_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "runs": runs})
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "unknown project endpoint")
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	var (
		runs []RunSummary
		err  error
	)
	project := r.URL.Query().Get("project")
	if project != "" {
		runs, err = s.service.ListRunsByProject(project)
	} else {
		runs, err = s.service.ListRuns()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleRunSubresource(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "run id is required")
		return
	}
	runID := parts[0]
	if !safeID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_run_id", "run id contains invalid path characters")
		return
	}
	if len(parts) == 1 {
		s.handleRunDetail(w, runID)
		return
	}
	if len(parts) > 2 {
		writeError(w, http.StatusNotFound, "not_found", "unknown run endpoint")
		return
	}

	resource := parts[1]
	switch resource {
	case "explorer":
		explorer, err := s.service.GetRunExplorer(runID)
		s.writeRunResult(w, runID, explorer, err)
	case "timeline", "events":
		events, err := s.service.GetEvents(runID)
		s.writeRunCollection(w, runID, "events", events, err)
	case "tool-calls":
		toolCalls, err := s.service.GetToolCalls(runID)
		s.writeRunCollection(w, runID, "tool_calls", toolCalls, err)
	case "policy-decisions":
		decisions, err := s.service.GetPolicyDecisions(runID)
		s.writeRunCollection(w, runID, "policy_decisions", decisions, err)
	case "contract-validations":
		validations, err := s.service.GetContractValidations(runID)
		s.writeRunCollection(w, runID, "contract_validations", validations, err)
	case "errors":
		errors, err := s.service.GetErrors(runID)
		s.writeRunCollection(w, runID, "errors", errors, err)
	case "lessons":
		lessons, err := s.service.GetLessons(runID)
		s.writeRunCollection(w, runID, "lessons", lessons, err)
	case "report":
		s.handleReport(w, runID)
	case "badcase":
		s.handleBadCase(w, runID)
	case "promotion-draft":
		s.handlePromotionDraft(w, runID)
	case "artifacts":
		artifacts, err := s.service.GetArtifacts(runID)
		s.writeRunCollection(w, runID, "artifacts", artifacts, err)
	case "stop-decisions":
		decisions, err := s.service.GetStopDecisions(runID)
		s.writeRunCollection(w, runID, "stop_decisions", decisions, err)
	case "gate-decisions":
		decisions, err := s.service.GetGateDecisions(runID)
		s.writeRunCollection(w, runID, "gate_decisions", decisions, err)
	case "hitl-reviews":
		reviews, err := s.service.GetHITLReviews(runID)
		s.writeRunCollection(w, runID, "hitl_reviews", reviews, err)
	case "context-packs":
		packs, err := s.service.GetContextPacks(runID)
		s.writeRunCollection(w, runID, "context_packs", packs, err)
	case "state":
		state, err := s.service.GetWorldState(runID)
		s.writeRunCollection(w, runID, "world_state", state, err)
	case "state-claims":
		claims, err := s.service.GetStateClaims(runID)
		s.writeRunCollection(w, runID, "state_claims", claims, err)
	case "eval-result":
		result, err := s.service.GetEvalResult(runID)
		s.writeRunResult(w, runID, result, err)
	case "scorecard":
		card, err := s.service.GetScorecard(runID)
		s.writeRunResult(w, runID, card, err)
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown run endpoint")
	}
}

func (s *Server) handleRunMutation(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "unknown run mutation endpoint")
		return
	}
	runID := parts[0]
	if !safeID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_run_id", "run id contains invalid path characters")
		return
	}
	switch parts[1] {
	case "gate-decisions":
		var decision model.GateDecision
		if err := json.NewDecoder(r.Body).Decode(&decision); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		decision.RunID = runID
		created, err := s.service.AppendGateDecision(decision)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "append_gate_decision_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	case "promotion-draft":
		draft, err := s.service.PromoteBadCase(runID)
		if err != nil {
			s.writeRunReadError(w, runID, err)
			return
		}
		writeJSON(w, http.StatusCreated, draft)
	case "hitl-reviews":
		var review model.HITLReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		review.RunID = runID
		created, err := s.service.AppendHITLReview(review)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "append_hitl_review_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown run mutation endpoint")
	}
}

func (s *Server) handleRunDetail(w http.ResponseWriter, runID string) {
	detail, err := s.service.GetRun(runID)
	s.writeRunResult(w, runID, detail, err)
}

func (s *Server) handleReport(w http.ResponseWriter, runID string) {
	report, err := s.service.GetReport(runID)
	if err != nil {
		s.writeRunReadError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "format": "markdown", "content": report})
}

func (s *Server) handleBadCase(w http.ResponseWriter, runID string) {
	badcase, err := s.service.GetBadCase(runID)
	if err != nil {
		s.writeRunReadError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "format": "yaml", "content": badcase})
}

func (s *Server) handlePromotionDraft(w http.ResponseWriter, runID string) {
	draft, err := s.service.GetPromotionDraft(runID)
	if err != nil {
		s.writeRunReadError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "format": "yaml", "content": draft})
}

func (s *Server) handleRepeatResult(w http.ResponseWriter, r *http.Request) {
	result, err := s.service.GetRepeatResult()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "repeat_result_not_found", "repeat_result.json was not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "read_repeat_result_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	registry, err := s.service.AssetRegistry()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_assets_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, registry)
}

func (s *Server) writeRunCollection(w http.ResponseWriter, runID, field string, value any, err error) {
	if err != nil {
		s.writeRunReadError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, field: value})
}

func (s *Server) writeRunResult(w http.ResponseWriter, runID string, value any, err error) {
	if err != nil {
		s.writeRunReadError(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) writeRunReadError(w http.ResponseWriter, runID string, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "run_not_found", fmt.Sprintf("run %s was not found", runID))
		return
	}
	writeError(w, http.StatusInternalServerError, "read_run_failed", err.Error())
}

func safeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", fmt.Sprint(recovered))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
