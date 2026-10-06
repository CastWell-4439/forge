package skillpack

import (
	"fmt"
	"strings"
)

// RenderDocument renders a pack as the text document skill.activate hands the
// model: the human half (readme) plus the machine half a model needs to
// FOLLOW the skill or REFUSE it when it does not apply. The CLI's `skills
// show` is a terminal summary; this is the full contract — steps and
// constraints are exactly the parts that make a skill actionable.
func RenderDocument(p Pack) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Skill: %s (version %s, status %s)\n",
		p.Metadata.ID, p.Metadata.Version, p.Metadata.Status)
	if p.Spec.Trigger.Description != "" {
		fmt.Fprintf(&b, "When to use: %s\n", p.Spec.Trigger.Description)
	}
	if len(p.Spec.Trigger.Keywords) > 0 {
		fmt.Fprintf(&b, "Keywords: %s\n", strings.Join(p.Spec.Trigger.Keywords, ", "))
	}

	if len(p.Spec.Steps) > 0 {
		b.WriteString("\nSteps:\n")
		for i, s := range p.Spec.Steps {
			if len(s.Tools) > 0 {
				fmt.Fprintf(&b, "  %d. %s (tools: %s)\n", i+1, s.Name, strings.Join(s.Tools, ", "))
			} else {
				fmt.Fprintf(&b, "  %d. %s\n", i+1, s.Name)
			}
		}
	}

	if len(p.Spec.Constraints) > 0 {
		b.WriteString("\nConstraints (negative knowledge — respect these):\n")
		for _, c := range p.Spec.Constraints {
			line := "  - do not: " + c.DoNot
			if c.Because != "" {
				line += " — because: " + c.Because
			}
			if c.Evidence != "" {
				line += " (evidence: " + c.Evidence + ")"
			}
			b.WriteString(line + "\n")
		}
	}

	if len(p.Spec.ToolPermissions) > 0 {
		fmt.Fprintf(&b, "\nTools this skill is allowed to use: %s\n", strings.Join(p.Spec.ToolPermissions, ", "))
	}
	if p.Spec.Eval.Suite != "" {
		fmt.Fprintf(&b, "Verified by eval suite %q (cases: %s)\n",
			p.Spec.Eval.Suite, strings.Join(p.Spec.Eval.Cases, ", "))
	}

	if readme := strings.TrimSpace(p.Readme); readme != "" {
		b.WriteString("\n")
		b.WriteString(readme)
		b.WriteString("\n")
	}
	return b.String()
}
