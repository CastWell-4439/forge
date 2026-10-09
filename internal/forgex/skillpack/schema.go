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
//
// The lifecycle is draft -> published -> deprecated. Deprecation is a STATUS,
// not a deletion: a skill encodes someone's reviewed judgement, and retiring it
// is a decision that can be revisited. Removing the file would destroy the
// evidence that the judgement was once made and why it stopped applying.
const (
	StatusDraft      = "draft"
	StatusPublished  = "published"
	StatusDeprecated = "deprecated"

	ReviewPending = "pending"
	ReviewDone    = "reviewed"
)

// DefaultStaleAfter is how long a reviewed skill is trusted before the review
// proposes looking at it again.
//
// It is a review threshold, not an expiry: crossing it produces a line in a
// report, never a status change. Half a year is long enough that the answer is
// usually "still fine" and short enough that a project's conventions have not
// moved twice underneath it.
const DefaultStaleAfter = 180 * 24 * time.Hour

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

	// ReviewedAt is when a human last confirmed this skill still applies.
	//
	// ReviewStatus says WHETHER it was reviewed; this says WHEN, and without a
	// time there is no way to tell a skill reviewed last week from one reviewed
	// two years ago — which is the entire question staleness asks. Zero means
	// never recorded, which reads as "unknown", not as "ancient".
	ReviewedAt time.Time `yaml:"reviewed_at,omitempty" json:"reviewed_at,omitempty"`
	// DeprecatedAt and DeprecationReason record a retirement.
	//
	// The reason is required when deprecating: a reviewer who finds a retired
	// skill needs to know why it stopped applying, and "deprecated" alone would
	// leave them to guess whether the world changed or the skill was wrong.
	DeprecatedAt      time.Time `yaml:"deprecated_at,omitempty" json:"deprecated_at,omitempty"`
	DeprecationReason string    `yaml:"deprecation_reason,omitempty" json:"deprecation_reason,omitempty"`

	// LastVerifiedAt and LastVerifyStatus record the outcome of running this
	// skill's own bound cases.
	//
	// The eval binding has always claimed that "this skill regressed" is a
	// checkable statement, and the checker existed — `skills verify` runs the
	// cases and reads the verdict. What was missing is that the answer went
	// nowhere: it was printed and forgotten, so the question could only be
	// answered by running everything again, and nobody could tell a skill
	// verified yesterday from one verified never.
	//
	// The record lives here rather than in a separate store because it is a
	// property OF the skill, and because a reader of the pack — a person, a
	// report, an operator — should see it without a second lookup.
	LastVerifiedAt time.Time `yaml:"last_verified_at,omitempty" json:"last_verified_at,omitempty"`
	// LastVerifyStatus is VerifyPassed, VerifyFailed, or "" when never run.
	LastVerifyStatus string `yaml:"last_verify_status,omitempty" json:"last_verify_status,omitempty"`
	// LastVerifyFailedCases names the cases that failed, so "it broke" comes
	// with "here is where". A status alone would leave the next reader to
	// re-run everything to find out which part moved.
	LastVerifyFailedCases []string `yaml:"last_verify_failed_cases,omitempty" json:"last_verify_failed_cases,omitempty"`
}

// Verification outcomes recorded on a skill.
const (
	// VerifyPassed means every bound case still matched its expected outcome.
	VerifyPassed = "passed"
	// VerifyFailed means at least one bound case no longer did.
	VerifyFailed = "failed"
)

// NeedsAttention reports whether this skill's last verification failed.
//
// An unverified skill answers "no": never having been checked is not the same
// as having been checked and failing, and treating the two alike would put
// every new skill into a regression report on the day it was published.
func (m Metadata) NeedsAttention() bool { return m.LastVerifyStatus == VerifyFailed }

// IsDeprecated reports whether this skill has been retired.
func (m Metadata) IsDeprecated() bool { return m.Status == StatusDeprecated }

// IsStale reports whether a reviewed skill is due for another look.
//
// It answers "no" for anything not reviewed, because a draft is not stale — it
// is unfinished, and the review's own rules already route it. Only a reviewed
// skill that has gone unreviewed long enough counts, which keeps the report
// about neglect rather than about age.
func (m Metadata) IsStale(now time.Time, staleAfter time.Duration) bool {
	if m.ReviewStatus != ReviewDone {
		return false
	}
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	// Never recorded means unknown, and an unknown age is not evidence of
	// neglect — reporting it would flag every skill that predates the field.
	if m.ReviewedAt.IsZero() {
		return false
	}
	return now.Sub(m.ReviewedAt) > staleAfter
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
