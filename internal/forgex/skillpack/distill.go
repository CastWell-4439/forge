package skillpack

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/cases"
	"github.com/castwell/forge/internal/forgex/model"
)

// Source is everything distillation reads out of one run directory.
//
// Every field has to come from artifacts the gates already produced: the
// replay verdict (D-07) and the eval result are the admission ticket, the
// tool call stream is the positive path, the lessons are the negative
// knowledge. Nothing here is model output.
type Source struct {
	RunDir    string
	RunID     string
	Verdict   cases.Verdict
	Eval      model.EvalResult
	ToolCalls []model.ToolCall
	Lessons   []model.Lesson
	Spec      cases.CaseSpec
}

// LoadSource reads a run directory and refuses anything that was not a
// verified replay of a registered case.
//
// The two gates are deliberate: distillation only makes sense from a run whose
// expected outcome matched and whose suite passed. That is exactly the
// admission rule decided for this design.
func LoadSource(runDir string, registry *cases.Registry) (*Source, error) {
	verdict, err := readJSON[cases.Verdict](filepath.Join(runDir, "replay_result.json"))
	if err != nil {
		return nil, fmt.Errorf("load source: %w (distill wants a run replayed with `forgex cases run`, which writes replay_result.json)", err)
	}
	if !verdict.Passed {
		return nil, fmt.Errorf("load source: run did not match its expected outcome (verdict for case %q failed)", verdict.CaseID)
	}

	eval, err := readJSON[model.EvalResult](filepath.Join(runDir, "eval_result.json"))
	if err != nil {
		return nil, fmt.Errorf("load source: %w", err)
	}
	if eval.Status != model.EvalPassed {
		return nil, fmt.Errorf("load source: suite %q status is %q, want passed", eval.SuiteID, eval.Status)
	}

	spec, err := registry.Find(verdict.CaseID)
	if err != nil {
		return nil, fmt.Errorf("load source: %w", err)
	}

	toolCalls, err := readJSONL[model.ToolCall](filepath.Join(runDir, "tool_calls.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("load source: %w", err)
	}
	lessons, err := readJSONL[model.Lesson](filepath.Join(runDir, "lessons.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load source: %w", err)
	}

	runID := eval.RunID
	if runID == "" {
		runID = filepath.Base(runDir)
	}

	return &Source{
		RunDir:    runDir,
		RunID:     runID,
		Verdict:   verdict,
		Eval:      eval,
		ToolCalls: toolCalls,
		Lessons:   lessons,
		Spec:      spec,
	}, nil
}

// Distill turns one verified source into a draft SkillPack.
//
// It is the single-source case of DistillMany, which owns the extraction and
// merge rules. The signature is unchanged so existing callers keep working.
func Distill(src *Source, now time.Time) Pack {
	if src == nil {
		return Pack{}
	}
	pack, err := DistillMany([]*Source{src}, MergeSpec{ID: src.Spec.ID}, now)
	if err != nil {
		// Only reachable for a source whose id is empty, which the loader
		// refuses; returning an empty pack keeps the old behaviour rather than
		// inventing a new failure mode for callers that cannot handle one.
		return Pack{}
	}
	return pack
}

// distillOne is the original single-source extraction.
//
// DistillMany delegates here when there is exactly one source, so a single-case
// pack is byte-for-byte what it always was apart from the version. The merge
// helpers generalise the same rules; letting them run on one source would risk
// changing every existing draft for no reason.
//
// The extraction rules are fixed so the same run always distils to the same
// bytes: tools keep first-occurrence order and are deduplicated, lessons become
// constraints in recorded order, and the readme starts as a template that the
// publish gate refuses.
func distillOne(src *Source, now time.Time) Pack {
	seenTools := make(map[string]bool)
	var steps []Step
	var permissions []string
	for _, call := range src.ToolCalls {
		if call.ToolName == "" || seenTools[call.ToolName] {
			continue
		}
		seenTools[call.ToolName] = true
		steps = append(steps, Step{Name: call.ToolName, Tools: []string{call.ToolName}})
		permissions = append(permissions, call.ToolName)
	}

	var constraints []Constraint
	var lessonIDs []string
	for _, lesson := range src.Lessons {
		constraints = append(constraints, Constraint{
			DoNot:    lesson.Title,
			Because:  strings.TrimSpace(lesson.Content),
			Evidence: lesson.ID,
		})
		lessonIDs = append(lessonIDs, lesson.ID)
	}

	return Pack{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			ID: src.Spec.ID,
			// Version is deliberately absent: DistillMany stamps it from the
			// finished content, so a value set here would be overwritten and
			// would only mislead a reader about where the version comes from.
			Status:        StatusDraft,
			ReviewStatus:  ReviewPending,
			SourceCases:   []string{src.Spec.ID},
			SourceRuns:    []string{src.RunID},
			SourceLessons: lessonIDs,
			GeneratedAt:   now.UTC(),
		},
		Spec: Spec{
			Trigger: Trigger{
				Description: src.Spec.Description,
			},
			Steps:           steps,
			Constraints:     constraints,
			ToolPermissions: permissions,
			Eval: EvalBinding{
				Suite: src.Spec.Suite,
				Cases: []string{src.Spec.ID},
			},
		},
		Readme: ReadmeTemplate,
	}
}

func readJSON[T any](path string) (T, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return zero, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// readJSONL parses an optional JSON Lines file; a missing file is an empty
// result, because a clean run writes no lessons file at all.
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []T
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		out = append(out, item)
	}
	return out, scanner.Err()
}
