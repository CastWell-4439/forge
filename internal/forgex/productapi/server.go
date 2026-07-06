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
}

// AssetSummary is a future-proof local asset registry projection.
type AssetSummary struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	RunID       string    `json:"run_id"`
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
	mux.HandleFunc("GET /api/v1/runs", s.handleRuns)
	mux.HandleFunc("GET /api/v1/runs/", s.handleRunSubresource)
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

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.service.ListRuns()
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
	case "artifacts":
		artifacts, err := s.service.GetArtifacts(runID)
		s.writeRunCollection(w, runID, "artifacts", artifacts, err)
	case "stop-decisions":
		decisions, err := s.service.GetStopDecisions(runID)
		s.writeRunCollection(w, runID, "stop_decisions", decisions, err)
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

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	assets, err := s.service.ListAssets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_assets_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace": "local",
		"assets":    assets,
	})
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
