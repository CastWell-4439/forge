package productapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	"github.com/castwell/forge/internal/forgex/scorecard"
	"github.com/castwell/forge/internal/forgex/storage"
)

// Service is the product-level read model boundary for the local Control Plane.
type Service struct {
	root   string
	layout storage.Layout
}

// Overview is the local workspace summary used by product console entry pages.
type Overview struct {
	Workspace       string         `json:"workspace"`
	Root            string         `json:"root"`
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
func (s *Service) Overview() (Overview, error) {
	runs, err := s.ListRuns()
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Workspace: "local", Root: s.root, Runs: len(runs), StatusHistogram: map[string]int{}}
	for _, run := range runs {
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
	return o, nil
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
		for _, artifact := range artifacts.Artifacts {
			assets = append(assets, AssetSummary{
				ID:          artifact.ID,
				Kind:        artifact.Type,
				RunID:       runID,
				Path:        artifact.URI,
				ContentType: artifact.Metadata["content_type"],
				CreatedAt:   artifact.CreatedAt,
			})
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].CreatedAt.After(assets[j].CreatedAt) })
	return assets, nil
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
