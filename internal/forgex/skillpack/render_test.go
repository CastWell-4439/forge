package skillpack

import (
	"strings"
	"testing"
)

// RenderDocument is what skill.activate hands the model: the machine half
// (steps, constraints, permissions) must be present, not just the readme —
// a skill without its negative knowledge is exactly the kind of half-answer
// that gets a constraint violated.
func TestRenderDocumentCarriesTheContract(t *testing.T) {
	p := Pack{
		APIVersion: "v1",
		Kind:       "SkillPack",
		Metadata:   Metadata{ID: "triage", Version: "1.2.0", Status: "published"},
		Readme:     "Use this when a report arrives with a stack trace.",
		Spec: Spec{
			Trigger:         Trigger{Description: "bug reports", Keywords: []string{"crash", "trace"}},
			Steps:           []Step{{Name: "reproduce", Tools: []string{"shell.run"}}, {Name: "bisect"}},
			Constraints:     []Constraint{{DoNot: "re-run the failing binary", Because: "it corrupts state", Evidence: "lesson-7"}},
			ToolPermissions: []string{"shell.run", "git.log"},
			Eval:            EvalBinding{Suite: "triage-suite", Cases: []string{"c1"}},
		},
	}

	doc := RenderDocument(p)

	for _, want := range []string{
		"# Skill: triage (version 1.2.0, status published)",
		"When to use: bug reports",
		"Keywords: crash, trace",
		"1. reproduce (tools: shell.run)",
		"2. bisect",
		"do not: re-run the failing binary",
		"because: it corrupts state",
		"evidence: lesson-7",
		"allowed to use: shell.run, git.log",
		"triage-suite",
		"stack trace",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered document is missing %q\n---\n%s", want, doc)
		}
	}
}

// A readme-less pack still renders its machine half: the contract is the spec,
// the prose is commentary.
func TestRenderDocumentWithoutReadme(t *testing.T) {
	doc := RenderDocument(Pack{
		Metadata: Metadata{ID: "bare", Version: "0.1.0", Status: "draft"},
		Spec:     Spec{Trigger: Trigger{Description: "nothing"}},
	})
	if !strings.Contains(doc, "# Skill: bare") {
		t.Errorf("header missing:\n%s", doc)
	}
	if !strings.Contains(doc, "When to use: nothing") {
		t.Errorf("trigger missing:\n%s", doc)
	}
}
