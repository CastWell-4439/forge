package productapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/promotion"
	"github.com/castwell/forge/internal/forgex/reliability"
	"github.com/castwell/forge/internal/forgex/scorecard"
	"github.com/castwell/forge/internal/forgex/storage"
)

// Service is the product-level read model boundary for the local Control Plane.
type Service struct {
	root   string
	layout storage.Layout
}

// Overview is the local workspace summary used by product console entry pages.
type ControlPlaneSummary struct {
	Workspace       string            `json:"workspace"`
	Root            string            `json:"root"`
	Ready           bool              `json:"ready"`
	Modules         map[string]string `json:"modules"`
	Overview        Overview          `json:"overview"`
	PendingHITL     int               `json:"pending_hitl"`
	GateDecisions   int               `json:"gate_decisions"`
	HumanGates      int               `json:"human_gates"`
	BadCases        int               `json:"badcases"`
	PromotionDrafts int               `json:"promotion_drafts"`
	RepeatAvailable bool              `json:"repeat_available"`
	GeneratedAt     time.Time         `json:"generated_at"`
}

// RunQuery describes supported local run list filters.
type RunQuery struct {
	Project string
	Status  string
	Q       string
	Limit   int
	Offset  int
}

// RunSearchResult is the paged run list response.
type RunSearchResult struct {
	Runs   []RunSummary `json:"runs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

type Overview struct {
	Workspace       string         `json:"workspace"`
	Root            string         `json:"root"`
	Projects        int            `json:"projects"`
	Runs            int            `json:"runs"`
	ActiveRuns      int            `json:"active_runs"`
	SucceededRuns   int            `json:"succeeded_runs"`
	FailedRuns      int            `json:"failed_runs"`
	StoppedRuns     int            `json:"stopped_runs"`
	PausedRuns      int            `json:"paused_runs"`
	EscalatedRuns   int            `json:"escalated_runs"`
	TotalErrors     int            `json:"total_errors"`
	TotalAssets     int            `json:"total_assets"`
	RecentRuns      []RunSummary   `json:"recent_runs"`
	StatusHistogram map[string]int `json:"status_histogram"`
}

// WorkspaceSummary describes one logical local Control Plane workspace.
type WorkspaceSummary struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Mode          string    `json:"mode"`
	Root          string    `json:"root"`
	Projects      int       `json:"projects"`
	Runs          int       `json:"runs"`
	Assets        int       `json:"assets"`
	Errors        int       `json:"errors"`
	LastRunAt     time.Time `json:"last_run_at,omitempty"`
	Derived       bool      `json:"derived"`
	SchemaVersion string    `json:"schema_version"`
}

// ProjectSummary is a product-level grouping derived from TaskPacket metadata.
type ProjectSummary struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	WorkspaceID     string         `json:"workspace_id"`
	Runs            int            `json:"runs"`
	ActiveRuns      int            `json:"active_runs"`
	SucceededRuns   int            `json:"succeeded_runs"`
	FailedRuns      int            `json:"failed_runs"`
	StoppedRuns     int            `json:"stopped_runs"`
	PausedRuns      int            `json:"paused_runs"`
	EscalatedRuns   int            `json:"escalated_runs"`
	Assets          int            `json:"assets"`
	Errors          int            `json:"errors"`
	LastRunAt       time.Time      `json:"last_run_at,omitempty"`
	StatusHistogram map[string]int `json:"status_histogram"`
	Derived         bool           `json:"derived"`
}

// AssetRegistry groups all local assets with workspace/project facets.
type AssetRegistry struct {
	Workspace string         `json:"workspace"`
	Assets    []AssetSummary `json:"assets"`
	ByKind    map[string]int `json:"by_kind"`
	ByProject map[string]int `json:"by_project"`
}

// RunExplorer is the product-level aggregate view for one run.
type RunExplorer struct {
	Detail              RunDetail                  `json:"detail"`
	Events              []model.Event              `json:"events"`
	ToolCalls           []model.ToolCall           `json:"tool_calls"`
	PolicyDecisions     []model.PolicyDecision     `json:"policy_decisions"`
	ContractValidations []model.ContractValidation `json:"contract_validations"`
	Errors              []model.ErrorEnvelope      `json:"errors"`
	Lessons             []model.Lesson             `json:"lessons"`
	Artifacts           []model.ArtifactRecord     `json:"artifacts"`
	StopDecisions       []model.StopDecision       `json:"stop_decisions"`
	GateDecisions       []model.GateDecision       `json:"gate_decisions"`
	HITLReviews         []model.HITLReview         `json:"hitl_reviews"`
	ContextPacks        []model.ContextPack        `json:"context_packs"`
	StateClaims         []model.StateClaim         `json:"state_claims"`
	WorldState          model.WorldState           `json:"world_state"`
	EvalResult          *model.EvalResult          `json:"eval_result,omitempty"`
	Scorecard           *scorecard.Scorecard       `json:"scorecard,omitempty"`
}

// NewService creates a local product read service.
func NewService(root string) *Service {
	if root == "" {
		root = ".forgex"
	}
	return &Service{root: root, layout: storage.NewLayout(root)}
}

// Root returns the configured artifact root.
func (s *Service) Root() string { return s.root }

// Layout returns the local artifact layout.
func (s *Service) Layout() storage.Layout { return s.layout }

// Overview returns a local workspace summary for Product Console.
func (s *Service) Summary() (ControlPlaneSummary, error) {
	overview, err := s.Overview()
	if err != nil {
		return ControlPlaneSummary{}, err
	}
	runs, err := s.ListRuns()
	if err != nil {
		return ControlPlaneSummary{}, err
	}
	summary := ControlPlaneSummary{
		Workspace:   "local",
		Root:        s.root,
		Ready:       true,
		Overview:    overview,
		GeneratedAt: time.Now().UTC(),
		Modules: map[string]string{
			"product_api":       "ready",
			"run_explorer":      "ready",
			"workspace_project": "ready",
			"asset_registry":    "ready",
			"runtime_gate":      "shadow_artifact_ready",
			"hitl":              "local_artifact_ready",
			"badcase_promotion": "ready",
			"repeat_result":     "optional",
			"ui":                "intentionally_not_in_scope",
		},
	}
	for _, run := range runs {
		gates, _ := s.GetGateDecisions(run.ID)
		reviews, _ := s.GetHITLReviews(run.ID)
		if _, err := s.GetBadCase(run.ID); err == nil {
			summary.BadCases++
		}
		if _, err := s.GetPromotionDraft(run.ID); err == nil {
			summary.PromotionDrafts++
		}
		summary.GateDecisions += len(gates)
		for _, gate := range gates {
			if gate.NeedsHuman {
				summary.HumanGates++
			}
		}
		for _, review := range reviews {
			if review.Status == model.HITLReviewPending {
				summary.PendingHITL++
			}
		}
	}
	if _, err := s.GetRepeatResult(); err == nil {
		summary.RepeatAvailable = true
	}
	return summary, nil
}

func (s *Service) Overview() (Overview, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return Overview{}, err
	}
	projects := map[string]struct{}{}
	o := Overview{Workspace: "local", Root: s.root, Runs: len(runs), StatusHistogram: map[string]int{}}
	for _, run := range runs {
		projects[projectID(run.Project)] = struct{}{}
		status := string(run.Status)
		o.StatusHistogram[status]++
		o.TotalErrors += run.ErrorCount
		o.TotalAssets += run.AssetCount
		switch run.Status {
		case model.RunPending, model.RunRunning:
			o.ActiveRuns++
		case model.RunSucceeded:
			o.SucceededRuns++
		case model.RunFailed:
			o.FailedRuns++
		case model.RunStopped:
			o.StoppedRuns++
		case model.RunPaused:
			o.PausedRuns++
		case model.RunEscalated:
			o.EscalatedRuns++
		}
	}
	limit := 10
	if len(runs) < limit {
		limit = len(runs)
	}
	o.RecentRuns = append([]RunSummary(nil), runs[:limit]...)
	o.Projects = len(projects)
	return o, nil
}

// ListWorkspaces returns the local workspace registry projection.
func (s *Service) ListWorkspaces() ([]WorkspaceSummary, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return nil, err
	}
	projects, err := s.ListProjects()
	if err != nil {
		return nil, err
	}
	assets, err := s.ListAssets()
	if err != nil {
		return nil, err
	}
	summary := WorkspaceSummary{
		ID:            "local",
		Name:          "Local Workspace",
		Mode:          "local",
		Root:          s.root,
		Projects:      len(projects),
		Runs:          len(runs),
		Assets:        len(assets),
		Derived:       true,
		SchemaVersion: "derived/v1",
	}
	for _, run := range runs {
		summary.Errors += run.ErrorCount
		if run.StartedAt.After(summary.LastRunAt) {
			summary.LastRunAt = run.StartedAt
		}
	}
	return []WorkspaceSummary{summary}, nil
}

// GetProject returns one derived project summary.
func (s *Service) GetProject(project string) (ProjectSummary, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return ProjectSummary{}, err
	}
	id := projectID(project)
	for _, item := range projects {
		if item.ID == id {
			return item, nil
		}
	}
	return ProjectSummary{}, os.ErrNotExist
}

// ListProjects groups runs by product project metadata.
func (s *Service) ListProjects() ([]ProjectSummary, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return nil, err
	}
	projects := map[string]*ProjectSummary{}
	for _, run := range runs {
		id := projectID(run.Project)
		project, ok := projects[id]
		if !ok {
			project = &ProjectSummary{ID: id, Name: projectName(run.Project), WorkspaceID: "local", StatusHistogram: map[string]int{}, Derived: true}
			projects[id] = project
		}
		project.Runs++
		project.Assets += run.AssetCount
		project.Errors += run.ErrorCount
		if run.StartedAt.After(project.LastRunAt) {
			project.LastRunAt = run.StartedAt
		}
		status := string(run.Status)
		project.StatusHistogram[status]++
		switch run.Status {
		case model.RunPending, model.RunRunning:
			project.ActiveRuns++
		case model.RunSucceeded:
			project.SucceededRuns++
		case model.RunFailed:
			project.FailedRuns++
		case model.RunStopped:
			project.StoppedRuns++
		case model.RunPaused:
			project.PausedRuns++
		case model.RunEscalated:
			project.EscalatedRuns++
		}
	}
	out := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		out = append(out, *project)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastRunAt.After(out[j].LastRunAt) })
	return out, nil
}

// SearchRuns returns filtered and paged local run summaries.
func (s *Service) SearchRuns(query RunQuery) (RunSearchResult, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return RunSearchResult{}, err
	}
	filtered := make([]RunSummary, 0, len(runs))
	needle := strings.ToLower(strings.TrimSpace(query.Q))
	status := strings.ToLower(strings.TrimSpace(query.Status))
	project := projectID(query.Project)
	for _, run := range runs {
		if query.Project != "" && projectID(run.Project) != project {
			continue
		}
		if status != "" && strings.ToLower(string(run.Status)) != status {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(strings.Join([]string{run.ID, run.TaskID, run.Name, run.Summary, run.Project}, " "))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		filtered = append(filtered, run)
	}
	total := len(filtered)
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := query.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return RunSearchResult{Runs: filtered[offset:end], Total: total, Limit: limit, Offset: offset}, nil
}

// ListRunsByProject returns local runs filtered by project id or name.
func (s *Service) ListRunsByProject(project string) ([]RunSummary, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return nil, err
	}
	id := projectID(project)
	filtered := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		if projectID(run.Project) == id {
			filtered = append(filtered, run)
		}
	}
	return filtered, nil
}

// ListRuns returns local run summaries sorted newest-first.
func (s *Service) ListRuns() ([]RunSummary, error) {
	entries, err := os.ReadDir(s.layout.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []RunSummary{}, nil
		}
		return nil, err
	}
	runs := make([]RunSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !safeID(entry.Name()) {
			continue
		}
		runID := entry.Name()
		var run model.Run
		if err := readJSON(s.layout.RunFile(runID), &run); err != nil {
			continue
		}
		artifacts, _ := s.LoadArtifacts(runID)
		summary := RunSummary{
			ID:         run.ID,
			TaskID:     run.TaskID,
			Name:       run.Name,
			Status:     run.Status,
			StartedAt:  run.StartedAt,
			EndedAt:    run.EndedAt,
			Summary:    run.Summary,
			Workspace:  "local",
			AssetCount: len(artifacts.Artifacts),
			ErrorCount: len(artifacts.Errors),
		}
		if artifacts.TaskPacket.Metadata != nil {
			summary.Project = artifacts.TaskPacket.Metadata["project"]
		}
		runs = append(runs, summary)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	return runs, nil
}

// GetRun returns the product detail projection for one run.
func (s *Service) GetRun(runID string) (RunDetail, error) {
	artifacts, err := s.LoadArtifacts(runID)
	if err != nil {
		return RunDetail{}, err
	}
	lessons, err := s.GetLessons(runID)
	if err != nil {
		return RunDetail{}, err
	}
	gateDecisions, err := s.GetGateDecisions(runID)
	if err != nil {
		return RunDetail{}, err
	}
	hitlReviews, err := s.GetHITLReviews(runID)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{
		Run:        artifacts.Run,
		TaskPacket: artifacts.TaskPacket,
		Workspace:  "local",
		Project:    artifacts.TaskPacket.Metadata["project"],
		Metrics: RunMetrics{
			Events:              len(artifacts.Events),
			ToolCalls:           len(artifacts.ToolCalls),
			PolicyDecisions:     len(artifacts.PolicyDecisions),
			ContractValidations: len(artifacts.ContractValidations),
			Errors:              len(artifacts.Errors),
			Lessons:             len(lessons),
			Artifacts:           len(artifacts.Artifacts),
			StopDecisions:       len(artifacts.StopDecisions),
			GateDecisions:       len(gateDecisions),
			HITLReviews:         len(hitlReviews),
		},
	}, nil
}

// GetRunExplorer returns the aggregate Run Explorer view for one run.
func (s *Service) GetRunExplorer(runID string) (RunExplorer, error) {
	artifacts, err := s.LoadArtifacts(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	detail, err := s.GetRun(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	lessons, err := s.GetLessons(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	contextPacks, err := s.GetContextPacks(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	gateDecisions, err := s.GetGateDecisions(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	hitlReviews, err := s.GetHITLReviews(runID)
	if err != nil {
		return RunExplorer{}, err
	}
	explorer := RunExplorer{
		Detail:              detail,
		Events:              artifacts.Events,
		ToolCalls:           artifacts.ToolCalls,
		PolicyDecisions:     artifacts.PolicyDecisions,
		ContractValidations: artifacts.ContractValidations,
		Errors:              artifacts.Errors,
		Lessons:             lessons,
		Artifacts:           artifacts.Artifacts,
		StopDecisions:       artifacts.StopDecisions,
		GateDecisions:       gateDecisions,
		HITLReviews:         hitlReviews,
		ContextPacks:        contextPacks,
		StateClaims:         artifacts.StateClaims,
		WorldState:          artifacts.WorldState,
	}
	if evalResult, err := s.GetEvalResult(runID); err == nil {
		explorer.EvalResult = &evalResult
	}
	if scorecardResult, err := s.GetScorecard(runID); err == nil {
		explorer.Scorecard = &scorecardResult
	}
	return explorer, nil
}

// LoadArtifacts reads the canonical local run artifacts.
func (s *Service) LoadArtifacts(runID string) (forgexeval.RunArtifacts, error) {
	if !safeID(runID) {
		return forgexeval.RunArtifacts{}, fmt.Errorf("invalid run id: %s", runID)
	}
	return forgexeval.LoadRunArtifacts(s.layout.RunDir(runID))
}

func (s *Service) GetEvents(runID string) ([]model.Event, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.Events, err
}

func (s *Service) GetToolCalls(runID string) ([]model.ToolCall, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.ToolCalls, err
}

func (s *Service) GetPolicyDecisions(runID string) ([]model.PolicyDecision, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.PolicyDecisions, err
}

func (s *Service) GetContractValidations(runID string) ([]model.ContractValidation, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.ContractValidations, err
}

func (s *Service) GetErrors(runID string) ([]model.ErrorEnvelope, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.Errors, err
}

func (s *Service) GetArtifacts(runID string) ([]model.ArtifactRecord, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.Artifacts, err
}

func (s *Service) GetStopDecisions(runID string) ([]model.StopDecision, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.StopDecisions, err
}

func (s *Service) GetGateDecisions(runID string) ([]model.GateDecision, error) {
	if !safeID(runID) {
		return nil, fmt.Errorf("invalid run id: %s", runID)
	}
	return readJSONL[model.GateDecision](s.layout.GateDecisionsFile(runID))
}

func (s *Service) GetHITLReviews(runID string) ([]model.HITLReview, error) {
	if !safeID(runID) {
		return nil, fmt.Errorf("invalid run id: %s", runID)
	}
	return readJSONL[model.HITLReview](s.layout.HITLReviewsFile(runID))
}

func (s *Service) AppendGateDecision(decision model.GateDecision) (model.GateDecision, error) {
	if !safeID(decision.RunID) {
		return model.GateDecision{}, fmt.Errorf("invalid run id: %s", decision.RunID)
	}
	if strings.TrimSpace(decision.ID) == "" {
		decision.ID = fmt.Sprintf("gate-%s-%d", decision.RunID, time.Now().UTC().UnixNano())
	}
	if decision.Mode == "" {
		decision.Mode = model.GateModeShadow
	}
	if decision.Action == "" {
		decision.Action = model.GateActionAllow
	}
	if decision.CreatedAt.IsZero() {
		decision.CreatedAt = time.Now().UTC()
	}
	if decision.Mode == model.GateModeShadow && decision.Action != model.GateActionAllow {
		decision.NeedsHuman = true
	}
	store := storage.NewFileStore(s.root)
	if err := store.AppendGateDecision(context.Background(), decision); err != nil {
		return model.GateDecision{}, err
	}
	return decision, nil
}

func (s *Service) AppendHITLReview(review model.HITLReview) (model.HITLReview, error) {
	if !safeID(review.RunID) {
		return model.HITLReview{}, fmt.Errorf("invalid run id: %s", review.RunID)
	}
	if strings.TrimSpace(review.ID) == "" {
		review.ID = fmt.Sprintf("hitl-%s-%d", review.RunID, time.Now().UTC().UnixNano())
	}
	if review.Status == "" {
		review.Status = model.HITLReviewPending
	}
	if review.CreatedAt.IsZero() {
		review.CreatedAt = time.Now().UTC()
	}
	if review.Status != model.HITLReviewPending && review.ResolvedAt.IsZero() {
		review.ResolvedAt = time.Now().UTC()
	}
	store := storage.NewFileStore(s.root)
	if err := store.AppendHITLReview(context.Background(), review); err != nil {
		return model.HITLReview{}, err
	}
	return review, nil
}

func (s *Service) GetContextPacks(runID string) ([]model.ContextPack, error) {
	if !safeID(runID) {
		return nil, fmt.Errorf("invalid run id: %s", runID)
	}
	return readJSONL[model.ContextPack](s.layout.ContextPacksFile(runID))
}

func (s *Service) GetWorldState(runID string) (model.WorldState, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.WorldState, err
}

func (s *Service) GetStateClaims(runID string) ([]model.StateClaim, error) {
	artifacts, err := s.LoadArtifacts(runID)
	return artifacts.StateClaims, err
}

func (s *Service) GetLessons(runID string) ([]model.Lesson, error) {
	if !safeID(runID) {
		return nil, fmt.Errorf("invalid run id: %s", runID)
	}
	return readJSONL[model.Lesson](s.layout.LessonsFile(runID))
}

func (s *Service) GetReport(runID string) (string, error) {
	if !safeID(runID) {
		return "", fmt.Errorf("invalid run id: %s", runID)
	}
	data, err := os.ReadFile(s.layout.ReportFile(runID))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) GetBadCase(runID string) (string, error) {
	if !safeID(runID) {
		return "", fmt.Errorf("invalid run id: %s", runID)
	}
	data, err := os.ReadFile(s.layout.BadCaseFile(runID))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) PromoteBadCase(runID string) (promotion.Draft, error) {
	if !safeID(runID) {
		return promotion.Draft{}, fmt.Errorf("invalid run id: %s", runID)
	}
	runDir := s.layout.RunDir(runID)
	return promotion.Promote(runDir, filepath.Join(runDir, "promotion_draft.yaml"))
}

func (s *Service) GetPromotionDraft(runID string) (string, error) {
	if !safeID(runID) {
		return "", fmt.Errorf("invalid run id: %s", runID)
	}
	data, err := os.ReadFile(filepath.Join(s.layout.RunDir(runID), "promotion_draft.yaml"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) GetRepeatResult() (reliability.RepeatResult, error) {
	var result reliability.RepeatResult
	if err := readJSON(filepath.Join(s.root, "repeat_result.json"), &result); err != nil {
		return reliability.RepeatResult{}, err
	}
	return result, nil
}

func (s *Service) GetEvalResult(runID string) (model.EvalResult, error) {
	if !safeID(runID) {
		return model.EvalResult{}, fmt.Errorf("invalid run id: %s", runID)
	}
	var result model.EvalResult
	if err := readJSON(filepath.Join(s.layout.RunDir(runID), "eval_result.json"), &result); err != nil {
		return model.EvalResult{}, err
	}
	return result, nil
}

func (s *Service) GetScorecard(runID string) (scorecard.Scorecard, error) {
	if !safeID(runID) {
		return scorecard.Scorecard{}, fmt.Errorf("invalid run id: %s", runID)
	}
	var card scorecard.Scorecard
	if err := readJSON(filepath.Join(s.layout.RunDir(runID), "scorecard.json"), &card); err != nil {
		return scorecard.Scorecard{}, err
	}
	return card, nil
}

// AssetRegistry returns the local product asset registry projection.
func (s *Service) AssetRegistry() (AssetRegistry, error) {
	assets, err := s.ListAssets()
	if err != nil {
		return AssetRegistry{}, err
	}
	registry := AssetRegistry{Workspace: "local", Assets: assets, ByKind: map[string]int{}, ByProject: map[string]int{}}
	for _, asset := range assets {
		registry.ByKind[asset.Kind]++
		registry.ByProject[projectID(asset.Project)]++
	}
	return registry, nil
}

// ListAssets returns the local asset registry projection.
func (s *Service) ListAssets() ([]AssetSummary, error) {
	entries, err := os.ReadDir(s.layout.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []AssetSummary{}, nil
		}
		return nil, err
	}
	var assets []AssetSummary
	for _, entry := range entries {
		if !entry.IsDir() || !safeID(entry.Name()) {
			continue
		}
		runID := entry.Name()
		artifacts, err := s.LoadArtifacts(runID)
		if err != nil {
			continue
		}
		project := artifacts.TaskPacket.Metadata["project"]
		for _, artifact := range artifacts.Artifacts {
			assets = append(assets, AssetSummary{
				ID:          artifact.ID,
				Kind:        artifact.Type,
				RunID:       runID,
				Project:     projectName(project),
				Path:        artifact.URI,
				ContentType: artifact.Metadata["content_type"],
				CreatedAt:   artifact.CreatedAt,
			})
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].CreatedAt.After(assets[j].CreatedAt) })
	return assets, nil
}

func projectID(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return "default"
	}
	id := strings.ToLower(project)
	id = strings.ReplaceAll(id, " ", "-")
	id = strings.ReplaceAll(id, "_", "-")
	return id
}

func projectName(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return "default"
	}
	return project
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readJSONL[T any](path string) ([]T, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return []T{}, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	items := make([]T, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
