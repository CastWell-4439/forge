// Package judge scores whether a run's output meets a declared acceptance.
//
// Its shape is decided by one architectural choice: **the workflow owns the
// verdict, this package only measures.** The handler reports a score and a
// reason; it is not told the threshold, so it has no way to decide its own
// outcome. The comparison lives in the workflow declaration, where it can be
// read, reviewed, replayed from the event log, and overridden by a human — none
// of which is true of a decision buried in a prompt.
//
// Why that matters rather than being a stylistic preference: if the scorer
// decided pass/fail, "did this pass" would be a model output, and the system's
// only constraint on the model would be the model's own judgement. An
// invariant this project holds is that constraint lives outside the model. A
// scorer that cannot see the bar is the mechanical form of that rule.
//
// The other decision is the order of the two checks, and it is a correctness
// property rather than an optimisation:
//
//  1. static  — are the declared artifacts there?     (mechanical, unambiguous)
//  2. dynamic — is what they contain any good?        (a judgement)
//
// Static runs first because a model given incomplete material tends to score
// something plausible instead of reporting that the input was missing. Checking
// completeness mechanically first means the failure names what is absent, and
// the quality question is only asked when it has an answer. It also means a
// missing artifact costs no model call.
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/castwell/forge/internal/agent/core"
)

// DefaultMaxReason caps a recorded reason. The reason is evidence a person reads;
// beyond a few hundred characters it is a transcript, and the transcript is in
// the run's own log.
const DefaultMaxReason = 2000

// Config configures the judge worker.
type Config struct {
	// Workspace is the directory artifact paths are resolved against. Empty
	// means the process working directory, which is what a worker running in
	// its own sandbox wants.
	Workspace string
	// MaxReason caps the recorded reason in bytes. Zero means DefaultMaxReason.
	MaxReason int
	// Model settings for the scoring call.
	Model       string
	Temperature float64
	MaxTokens   int
}

// DefaultConfig returns the configuration the worker uses when none is given.
func DefaultConfig() Config {
	return Config{
		Temperature: 0,
		MaxTokens:   1024,
		MaxReason:   DefaultMaxReason,
	}
}

// Worker scores an output against a declared acceptance.
type Worker struct {
	config Config
	llm    core.LLMClient
}

// NewWorker creates the judge worker. A nil llm is accepted and reported at use:
// a deployment that wants static checks only is a legitimate configuration, and
// it should get a clear message rather than a panic at construction.
func NewWorker(cfg Config, llm core.LLMClient) *Worker {
	if cfg.MaxReason <= 0 {
		cfg.MaxReason = DefaultMaxReason
	}
	return &Worker{config: cfg, llm: llm}
}

// Score is the worker's output. It is deliberately a measurement, not a verdict:
// there is no `passed` field, because the worker is not the party that decides.
//
// Anything reading this needs the two facts separately — how good it is, and
// what the bar was — so that a threshold can be changed without re-running the
// scorer, and so a human reviewing the run can see both.
type Score struct {
	// Score is 0..1. It is the measurement.
	Score float64 `json:"score"`
	// Reason explains the score in prose, and names what is missing when the
	// static check failed. This is the evidence a person reads.
	Reason string `json:"reason,omitempty"`
	// Suggestion is what would raise the score. It travels into a retry so the
	// next attempt is a correction rather than a repetition.
	Suggestion string `json:"suggestion,omitempty"`
	// Artifacts lists what was checked and whether each was found. Present even
	// when everything was found, so "all present" is stated rather than inferred
	// from silence.
	Artifacts []ArtifactCheck `json:"artifacts,omitempty"`
	// Scored reports whether the model was consulted. False means the static
	// check failed and the quality question was never asked — a reader can tell
	// "measured zero" from "not measured".
	Scored bool `json:"scored"`
	// Model is the model that produced the score, for audit.
	Model string `json:"model,omitempty"`
}

// ArtifactCheck is one declared artifact and whether it was found.
type ArtifactCheck struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
	// Detail explains a miss (permission, a broken glob) when there is more to
	// say than "absent".
	Detail string `json:"detail,omitempty"`
}

// Execute runs one scoring pass. action must be "run". Params:
//
//	task        the original task, for context (optional)
//	acceptance  what "done" means — criteria, checks, artifacts, and the
//	            subject's text if it is not given as `subject`
//	subject     the material to score, as text (optional; falls back to
//	            acceptance.criteria + checks being the only context)
//
// It returns the Score as JSON. A failure here means the measurement could not
// be taken — a missing acceptance, or a model that could not be reached — which
// is a different thing from a low score, and is reported as an error so the two
// are never confused.
func (w *Worker) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	if action != "run" {
		return "", fmt.Errorf("judge worker: unknown action %q (supported: run)", action)
	}

	acc := readAcceptance(params)
	if acc.Criteria == "" && len(acc.Checks) == 0 {
		// Nothing to score against. This is a defect in the workflow, not a low
		// score: an acceptance that says nothing cannot be met or missed.
		return "", fmt.Errorf("judge worker: no acceptance declared (need criteria or checks)")
	}

	out := Score{
		Artifacts: checkArtifacts(acc.Artifacts, w.config.Workspace),
	}

	// Static first. A miss is a zero with the missing paths named, and no model
	// call: there is nothing to assess, and asking anyway invites an answer about
	// material that is not there.
	if missing := missingArtifacts(out.Artifacts); len(missing) > 0 {
		out.Score = 0
		out.Reason = "声明的产物不齐全，未进行质量评估：" + strings.Join(missing, "、")
		out.Suggestion = "产出缺失的产物后再评估：" + strings.Join(missing, "、")
		return marshalScore(out, w.config.MaxReason)
	}

	// Dynamic second, and only now.
	if w.llm == nil {
		return "", fmt.Errorf("judge worker: no model configured, so the quality score cannot be taken " +
			"(the static artifact check passed; this deployment has no LLM)")
	}

	subject := readString(params, "subject")
	raw, err := w.llm.Chat(ctx, []core.Message{
		{Role: "system", Content: scoringPrompt},
		{Role: "user", Content: scoringInput(params, acc, subject, out.Artifacts)},
	})
	if err != nil {
		return "", fmt.Errorf("judge worker: scoring call failed: %w", err)
	}

	parsed, err := parseScoring(raw)
	if err != nil {
		return "", fmt.Errorf("judge worker: %w", err)
	}

	out.Score = clamp01(parsed.Score)
	out.Reason = parsed.Reason
	out.Suggestion = parsed.Suggestion
	out.Scored = true
	out.Model = w.config.Model

	return marshalScore(out, w.config.MaxReason)
}

// scoringPrompt asks for a measurement and nothing else.
//
// It does not mention a threshold, a pass mark, or what to do about a low score.
// That is not an omission: the scorer must not be able to decide its own
// outcome, and the surest way to guarantee that is to leave it uninformed.
const scoringPrompt = `你是一个质量评估员。根据验收标准，为被评估的产出打一个 0 到 1 之间的分数。

输出 JSON：
- score: 0 到 1 之间的小数，表示产出满足验收标准的程度
- reason: 为什么是这个分数，指出具体依据
- suggestion: 如果分数不高，要做什么才能提高；分数高就留空

规则：
1. 只根据给出的验收标准打分，不要引入额外的期望
2. 依据要具体到产出里的内容，不要泛泛而谈
3. 不要判断"合格/不合格"——你只负责打分，是否达标由工作流决定
4. 只输出纯 JSON，不要 markdown 代码块，不要解释文字`

// scoringInput assembles what the model sees.
func scoringInput(params map[string]any, acc acceptanceInput, subject string, artifacts []ArtifactCheck) string {
	var b strings.Builder

	b.WriteString("验收标准：\n")
	b.WriteString(acc.Criteria)
	if len(acc.Checks) > 0 {
		b.WriteString("\n\n检查项：\n")
		for _, c := range acc.Checks {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}

	if s := strings.TrimSpace(subject); s != "" {
		b.WriteString("\n被评估的产出：\n")
		b.WriteString(s)
	} else if task := readString(params, "task"); task != "" {
		// No material to read, only the task it was meant to satisfy. Saying so
		// is better than letting the model score an empty page: it will report a
		// low score for the right reason instead of inventing content.
		b.WriteString("\n被评估的产出：未提供。原始任务：\n")
		b.WriteString(task)
	}

	if present := presentArtifacts(artifacts); len(present) > 0 {
		b.WriteString("\n已确认存在的产物：\n")
		for _, p := range present {
			fmt.Fprintf(&b, "- %s\n", p.Path)
		}
	}

	return b.String()
}

// scoringResponse is what the model is asked to produce.
type scoringResponse struct {
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
	Suggestion string  `json:"suggestion"`
}

// parseScoring extracts the score from the model's reply.
func parseScoring(raw string) (scoringResponse, error) {
	var out scoringResponse

	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		if idx := strings.Index(text, "\n"); idx >= 0 {
			text = text[idx+1:]
		}
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
		text = strings.TrimSpace(text)
	}

	// The object is located rather than assumed to be the whole reply: models
	// preface JSON despite being told not to, and discarding a usable score over
	// a sentence of preamble would waste the call.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return out, fmt.Errorf("scoring reply contains no JSON object: %.120q", raw)
	}

	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return out, fmt.Errorf("scoring reply is not valid JSON: %w", err)
	}
	return out, nil
}

// acceptanceInput is the part of an acceptance the scorer needs. It is read
// through a map because the acceptance arrives from the workflow as params, and
// the shape is the one the planning package declares.
type acceptanceInput struct {
	Criteria  string
	Checks    []string
	Artifacts []string
}

// readAcceptance reads the acceptance from params.
//
// Two shapes are accepted, matching the planner: `acceptance` as a string (the
// criterion alone) and as an object with criteria, checks and artifacts. An
// author who wrote one sentence should not have to wrap it to be understood.
func readAcceptance(params map[string]any) acceptanceInput {
	var out acceptanceInput

	switch v := params["acceptance"].(type) {
	case string:
		out.Criteria = strings.TrimSpace(v)
	case map[string]any:
		out.Criteria, _ = v["criteria"].(string)
		out.Criteria = strings.TrimSpace(out.Criteria)
		out.Checks = readStrings(v["checks"])
		out.Artifacts = readStrings(v["artifacts"])
	}

	// Direct params win when given, so a workflow can pass them separately.
	if c := readString(params, "criteria"); c != "" {
		out.Criteria = c
	}
	if a := readStrings(params["artifacts"]); len(a) > 0 {
		out.Artifacts = a
	}

	return out
}

// checkArtifacts resolves each declared artifact and reports what it found.
//
// A glob is expanded: a requirement often says "one report per region" without
// naming the regions, and treating the pattern itself as a missing file would
// report a false absence.
func checkArtifacts(paths []string, workspace string) []ArtifactCheck {
	if len(paths) == 0 {
		return nil
	}

	checks := make([]ArtifactCheck, 0, len(paths))
	// Sorted so two runs over the same declaration produce the same report.
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)

	for _, p := range sorted {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		check := ArtifactCheck{Path: p}
		resolved := p
		if workspace != "" && !filepath.IsAbs(p) {
			resolved = filepath.Join(workspace, p)
		}

		if strings.ContainsAny(p, "*?[") {
			matches, err := filepath.Glob(resolved)
			switch {
			case err != nil:
				check.Detail = fmt.Sprintf("glob is malformed: %v", err)
			case len(matches) == 0:
				// Present stays false: a pattern matching nothing means the
				// artifacts it describes are not there.
				check.Detail = "no files match this pattern"
			default:
				check.Present = true
				check.Detail = fmt.Sprintf("%d file(s) match", len(matches))
			}
			checks = append(checks, check)
			continue
		}

		switch info, err := os.Stat(resolved); {
		case err != nil:
			if os.IsNotExist(err) {
				// No detail: "absent" is the whole story.
				break
			}
			check.Detail = err.Error()
		case info.IsDir():
			// A directory satisfies a declared artifact: some outputs are a
			// directory of files rather than one file.
			check.Present = true
		case info.Size() == 0:
			// An empty file is not an artifact. It exists, which is exactly why
			// presence alone would have called it present.
			check.Detail = "file is empty"
		default:
			check.Present = true
		}

		checks = append(checks, check)
	}
	return checks
}

// missingArtifacts returns the paths that were not found.
func missingArtifacts(checks []ArtifactCheck) []string {
	var out []string
	for _, c := range checks {
		if !c.Present {
			out = append(out, c.Path)
		}
	}
	return out
}

// presentArtifacts returns the paths that were found.
func presentArtifacts(checks []ArtifactCheck) []ArtifactCheck {
	var out []ArtifactCheck
	for _, c := range checks {
		if c.Present {
			out = append(out, c)
		}
	}
	return out
}

// marshalScore renders the score, capping the prose.
func marshalScore(s Score, maxReason int) (string, error) {
	if maxReason > 0 {
		s.Reason = truncate(s.Reason, maxReason)
		s.Suggestion = truncate(s.Suggestion, maxReason)
	}
	body, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("judge worker: marshal score: %w", err)
	}
	return string(body), nil
}

// clamp01 keeps a score inside its declared range. A model returning 7 or -1 is
// reporting on a scale it invented, and passing that through would make every
// threshold comparison meaningless.
func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// truncate shortens prose and marks that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// readString reads a trimmed string param.
func readString(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return strings.TrimSpace(s)
}

// readStrings reads a list of strings, accepting both []string and the []any a
// JSON or YAML decoder produces.
func readStrings(v any) []string {
	switch list := v.(type) {
	case []string:
		out := make([]string, 0, len(list))
		for _, s := range list {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}
