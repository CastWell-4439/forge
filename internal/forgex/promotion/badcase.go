package promotion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	forgexeval "github.com/castwell/forge/internal/forgex/eval"
	"github.com/castwell/forge/internal/forgex/model"
	"gopkg.in/yaml.v3"
)

// Draft is a human-reviewed regression case draft generated from one bad run.
type Draft struct {
	ID              string          `yaml:"id" json:"id"`
	Title           string          `yaml:"title" json:"title"`
	SourceRunID     string          `yaml:"source_run_id" json:"source_run_id"`
	ReviewRequired  bool            `yaml:"review_required" json:"review_required"`
	ReviewStatus    string          `yaml:"review_status" json:"review_status"`
	GeneratedAt     time.Time       `yaml:"generated_at" json:"generated_at"`
	FailureCategory string          `yaml:"failure_category,omitempty" json:"failure_category,omitempty"`
	Expected        ExpectedOutcome `yaml:"expected" json:"expected"`
	Replay          Replay          `yaml:"replay" json:"replay"`
	Notes           []string        `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// ExpectedOutcome is the draft expected outcome inferred from the source run.
type ExpectedOutcome struct {
	Status          string   `yaml:"status,omitempty" json:"status,omitempty"`
	FinalDecision   string   `yaml:"final_decision,omitempty" json:"final_decision,omitempty"`
	ErrorCategories []string `yaml:"error_categories,omitempty" json:"error_categories,omitempty"`
	LessonsMin      int      `yaml:"lessons_min,omitempty" json:"lessons_min,omitempty"`
}

// Replay holds enough information for a human to curate a replayable case.
type Replay struct {
	TaskPacket model.TaskPacket `yaml:"task_packet" json:"task_packet"`
}

type sourceBadCase struct {
	ID               string   `yaml:"id"`
	Title            string   `yaml:"title"`
	RunID            string   `yaml:"run_id"`
	FailureCategory  string   `yaml:"failure_category"`
	ExpectedDecision string   `yaml:"expected_decision"`
	Assertions       []string `yaml:"assertions"`
}

// Promote reads a ForgeX run directory and writes a human-review-required draft.
func Promote(runDir string, outPath string) (Draft, error) {
	if strings.TrimSpace(runDir) == "" {
		return Draft{}, fmt.Errorf("run dir is required")
	}
	if strings.TrimSpace(outPath) == "" {
		return Draft{}, fmt.Errorf("out path is required")
	}
	badCase, err := readBadCase(filepath.Join(runDir, "badcase.yaml"))
	if err != nil {
		return Draft{}, err
	}
	artifacts, err := forgexeval.LoadRunArtifacts(runDir)
	if err != nil {
		return Draft{}, err
	}
	draft := buildDraft(badCase, artifacts, countLessons(filepath.Join(runDir, "lessons.jsonl")))
	if err := writeDraft(outPath, draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func readBadCase(path string) (sourceBadCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sourceBadCase{}, fmt.Errorf("read badcase %s: %w", path, err)
	}
	var bc sourceBadCase
	if err := yaml.Unmarshal(data, &bc); err != nil {
		return sourceBadCase{}, fmt.Errorf("parse badcase %s: %w", path, err)
	}
	if strings.TrimSpace(bc.ID) == "" {
		return sourceBadCase{}, fmt.Errorf("badcase id is required")
	}
	return bc, nil
}

func buildDraft(bc sourceBadCase, artifacts forgexeval.RunArtifacts, lessonCount int) Draft {
	category := strings.TrimSpace(bc.FailureCategory)
	if category == "" && len(artifacts.Errors) > 0 {
		category = artifacts.Errors[0].Category
	}
	finalDecision := strings.TrimSpace(bc.ExpectedDecision)
	if finalDecision == "" && len(artifacts.StopDecisions) > 0 {
		finalDecision = string(artifacts.StopDecisions[len(artifacts.StopDecisions)-1].Action)
	}
	return Draft{
		ID:              sanitizeID(bc.ID),
		Title:           strings.TrimSpace(bc.Title),
		SourceRunID:     artifacts.Run.ID,
		ReviewRequired:  true,
		ReviewStatus:    "pending",
		GeneratedAt:     time.Now().UTC(),
		FailureCategory: category,
		Expected: ExpectedOutcome{
			Status:          string(artifacts.Run.Status),
			FinalDecision:   finalDecision,
			ErrorCategories: uniqueErrorCategories(artifacts.Errors),
			LessonsMin:      lessonCount,
		},
		Replay: Replay{TaskPacket: artifacts.TaskPacket},
		Notes: []string{
			"Draft only: review before adding to the golden case registry.",
			"Curate expected outcomes and eval assertions before committing as a regression case.",
		},
	}
}

func writeDraft(outPath string, draft Draft) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(draft)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, encoded, 0o644)
}

func uniqueErrorCategories(errors []model.ErrorEnvelope) []string {
	seen := make(map[string]struct{}, len(errors))
	var categories []string
	for _, e := range errors {
		cat := strings.TrimSpace(e.Category)
		if cat == "" {
			continue
		}
		if _, ok := seen[cat]; ok {
			continue
		}
		seen[cat] = struct{}{}
		categories = append(categories, cat)
	}
	return categories
}

func countLessons(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "forgex_badcase_draft"
	}
	id = strings.ToLower(id)
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(id)
}
