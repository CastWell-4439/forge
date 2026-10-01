// Package skillpack models a SkillPack: a versioned experience asset distilled
// from verified case replays and meant to be both read by a human and loaded by
// a machine.
//
// The package is deterministic by charter: distillation reads run artifacts and
// never asks a model to decide anything. Prose inside a pack (the readme) is a
// separate, injectable filling step so the ForgeX control plane keeps its
// zero-LLM property while a caller can still let a model draft the wording.
package skillpack

import (
	"fmt"
	"strings"
	"time"
)

// APIVersion and Kind tag the document format.
const (
	APIVersion = "forgex/v1"
	Kind       = "SkillPack"
)

// Pack statuses and review states.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"

	ReviewPending = "pending"
	ReviewDone    = "reviewed"
)

// ReadmeTemplateMarker tags the placeholder a distilled pack starts with.
// The publish gate rejects it, so a pack cannot ship until someone (or a model,
// via the fill step) has actually written the human-facing part.
const ReadmeTemplateMarker = "<!-- TODO"

// ReadmeTemplate is the starting text for a distilled pack's readme.
const ReadmeTemplate = ReadmeTemplateMarker + ` describe when to use this skill and how it works.
This block must be written by a human or filled by a model before publishing. -->`

// KnownWorkers is the workflow vocabulary a skill's tools are drawn from:
// the workers workflows actually dispatch to. A tool entry must look like
// "worker.action" with the worker from this list - anything else fails
// publish, which is where a trajectory recorded under a different name gets
// normalized by the reviewer.
var KnownWorkers = []string{"ai", "review", "database", "git", "mcp", "hitl", "shell", "claude_code"}

// Pack is one SkillPack document: one file, two halves - the machine-readable
// spec and the human-facing readme, kept together so they cannot drift apart.
type Pack struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
	Readme     string   `yaml:"readme" json:"readme"`
}

// Metadata carries identity, version and provenance.
type Metadata struct {
	ID            string    `yaml:"id" json:"id"`
	Version       string    `yaml:"version" json:"version"`
	Status        string    `yaml:"status" json:"status"`
	ReviewStatus  string    `yaml:"review_status" json:"review_status"`
	SourceCases   []string  `yaml:"source_cases,omitempty" json:"source_cases,omitempty"`
	SourceRuns    []string  `yaml:"source_runs,omitempty" json:"source_runs,omitempty"`
	SourceLessons []string  `yaml:"source_lessons,omitempty" json:"source_lessons,omitempty"`
	GeneratedAt   time.Time `yaml:"generated_at" json:"generated_at"`
}

// Spec is the machine-readable half.
type Spec struct {
	Trigger     Trigger      `yaml:"trigger" json:"trigger"`
	Steps       []Step       `yaml:"steps" json:"steps"`
	Constraints []Constraint `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	// ToolPermissions is the audit surface: every capability this skill asks for.
	ToolPermissions []string    `yaml:"tool_permissions" json:"tool_permissions"`
	Eval            EvalBinding `yaml:"eval" json:"eval"`
}

// Trigger says when the skill applies.
type Trigger struct {
	Description string   `yaml:"description" json:"description"`
	Keywords    []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`
}

// Step is one entry of the positive path: a name plus the tools it uses.
type Step struct {
	Name  string   `yaml:"name" json:"name"`
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// Constraint is negative knowledge carried over from a lesson.
type Constraint struct {
	DoNot    string `yaml:"do_not" json:"do_not"`
	Because  string `yaml:"because" json:"because"`
	Evidence string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// EvalBinding ties the skill to the cases that prove it, which is what makes
// "this skill regressed" a checkable statement instead of an opinion.
type EvalBinding struct {
	Suite string   `yaml:"suite" json:"suite"`
	Cases []string `yaml:"cases" json:"cases"`
}

// IsPlaceholder reports whether the readme is still the distilled template.
func (p Pack) IsPlaceholder() bool {
	return strings.Contains(p.Readme, ReadmeTemplateMarker)
}

// ToolIssues returns one message per tool reference that is not of the form
// "worker.action" with a known worker. It covers both the declared permissions
// and the tools named inside steps.
func (p Pack) ToolIssues() []string {
	known := make(map[string]bool, len(KnownWorkers))
	for _, w := range KnownWorkers {
		known[w] = true
	}
	check := func(where, tool string) []string {
		parts := strings.Split(tool, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return []string{fmt.Sprintf("%s: %q is not of the form worker.action", where, tool)}
		}
		if !known[parts[0]] {
			return []string{fmt.Sprintf("%s: %q uses worker %q, not one of %v", where, tool, parts[0], KnownWorkers)}
		}
		return nil
	}

	var issues []string
	for _, tool := range p.Spec.ToolPermissions {
		issues = append(issues, check("tool_permissions", tool)...)
	}
	for _, step := range p.Spec.Steps {
		for _, tool := range step.Tools {
			issues = append(issues, check("step "+step.Name, tool)...)
		}
	}
	return issues
}

// CaseExists answers whether a case id is present in the registry the caller
// supplied; injecting it keeps validation free of file access.
type CaseExists func(id string) bool

// Check runs the publish gates: the human-facing half must actually be written,
// the tool vocabulary must be the workflow vocabulary, and the eval binding must
// point at cases that exist. It returns one message per violation.
func (p Pack) Check(exists CaseExists) []string {
	var issues []string

	if strings.TrimSpace(p.Readme) == "" {
		issues = append(issues, "readme: the human-facing half is empty")
	} else if p.IsPlaceholder() {
		issues = append(issues, "readme: still the distillation template; write the actual description")
	}

	issues = append(issues, p.ToolIssues()...)

	if p.Spec.Eval.Suite == "" {
		issues = append(issues, "eval.suite: a SkillPack must name the suite that proves it")
	}
	if len(p.Spec.Eval.Cases) == 0 {
		issues = append(issues, "eval.cases: at least one source case is required")
	}
	if exists != nil {
		for _, id := range p.Spec.Eval.Cases {
			if !exists(id) {
				issues = append(issues, fmt.Sprintf("eval.cases: case %q does not exist in the registry", id))
			}
		}
	}
	return issues
}
