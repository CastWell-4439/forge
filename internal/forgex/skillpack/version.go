package skillpack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Versioning and merging for SkillPacks.
//
// Two gaps that turn out to be the same gap. A pack's Version was the constant
// "0.1.0", and Distill accepted exactly one Source, so a skill could not say
// which revision of which case it came from — nor which cases shaped it. Both
// are failures of the same thing: the link between a skill and its origins was
// not recorded.
//
// The version is therefore DERIVED FROM CONTENT rather than passed in and
// remembered. Re-distilling an unchanged case yields the same version, so
// nothing churns; changing the case yields a new one, so staleness is visible
// without anyone having to remember to bump a number.
//
// Semantic versioning was the alternative and it does not fit: major/minor/patch
// encode whether a change is breaking, and "breaking" has no definition for a
// skill — it is prose, constraints and a tool allow-list, and which part of that
// a consumer depends on is not knowable from here. A number nobody can justify
// is worse than a digest that means exactly one thing.

// DefaultBaseVersion is the base a distilled pack starts from.
//
// The base is the human's channel: a digest says "this content", it cannot say
// "this content, and I intend it to be the 2.x line". Merging or promoting
// deliberately is when someone sets it.
const DefaultBaseVersion = "0.1.0"

// MergeSpec describes the pack a merge should produce.
type MergeSpec struct {
	// ID is the merged pack's id. Required: a skill built from several cases is
	// none of them, so its name is a decision rather than a derivation.
	ID string
	// Base is the version's human part. Empty means DefaultBaseVersion.
	Base string
	// Suite overrides the eval suite. Empty takes the sources' common suite, or
	// the first one when they disagree — Disagreement is reported in the pack's
	// readme rather than silently resolved.
	Suite string
	// Description overrides the trigger description. Empty composes one from the
	// sources.
	Description string
}

// DistillMany builds one draft SkillPack from any number of verified sources.
//
// The merge is deterministic: sources are ordered by case id before anything is
// combined, so the same set always produces the same bytes and therefore the
// same version. Without that, a merged pack's version would change on every run
// and stop meaning anything.
//
// Conflicts are UNIONED, not resolved, with one deliberate exception. Steps and
// tool permissions accumulate, because a skill built from two cases must be able
// to do what either did. Constraints accumulate too — a constraint says "do not
// do this", so dropping one to make two sources agree would be discarding
// safety to tidy a document. What cannot be unioned is a contradiction between
// two constraints; that is left visible in the pack rather than guessed at,
// because nothing here can tell which case was right.
func DistillMany(sources []*Source, spec MergeSpec, now time.Time) (Pack, error) {
	live := make([]*Source, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return Pack{}, fmt.Errorf("distill: no sources")
	}

	id := strings.TrimSpace(spec.ID)
	if id == "" {
		if len(live) == 1 {
			id = live[0].Spec.ID
		}
		if id == "" {
			return Pack{}, fmt.Errorf("distill: a merged pack needs an id (no source id to fall back on)")
		}
	}

	// Order first, then merge. Everything below depends on this being stable.
	sort.SliceStable(live, func(i, j int) bool {
		if live[i].Spec.ID != live[j].Spec.ID {
			return live[i].Spec.ID < live[j].Spec.ID
		}
		return live[i].RunID < live[j].RunID
	})

	// Exactly one source goes down the original path, so a single-case pack is
	// byte-for-byte what it always was apart from the version. The merge helpers
	// generalise the same rules; letting them run on one source would risk
	// changing every existing draft for no reason.
	if len(live) == 1 {
		pack := distillOne(live[0], now)
		pack.Metadata.Version = VersionFor(pack, spec.Base)
		return pack, nil
	}

	pack := Pack{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			ID:            id,
			Status:        StatusDraft,
			ReviewStatus:  ReviewPending,
			SourceCases:   caseIDs(live),
			SourceRuns:    runIDs(live),
			SourceLessons: lessonIDs(live),
			GeneratedAt:   now.UTC(),
		},
		Spec: Spec{
			Trigger:         Trigger{Description: mergedDescription(live, spec.Description)},
			Steps:           mergedSteps(live),
			Constraints:     mergedConstraints(live),
			ToolPermissions: mergedPermissions(live),
			Eval: EvalBinding{
				Suite: mergedSuite(live, spec.Suite),
				Cases: caseIDs(live),
			},
		},
		Readme: ReadmeTemplate,
	}

	// The version is stamped last, over the finished content, so it describes
	// what the pack IS rather than what was intended when it was built.
	pack.Metadata.Version = VersionFor(pack, spec.Base)
	return pack, nil
}

// VersionFor derives a pack's version from its content.
//
// The format is `<base>+<digest8>`, using semver's build-metadata syntax: the
// part before `+` is the human's channel and the part after is what the content
// actually is. Two packs with the same base and the same content get the same
// version, which is what makes "has the source changed since this was
// published?" answerable by comparing one string.
func VersionFor(p Pack, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = DefaultBaseVersion
	}
	return base + "+" + ContentDigest(p)
}

// ContentDigest is a short digest of everything that makes a pack what it is.
//
// It deliberately covers only the pack's meaning. The lifecycle fields — status,
// review status, verification bookkeeping, and the generation time — are NOT
// included, because publishing or re-distilling at a later moment would then
// change the version, and a version that moves for reasons unrelated to the
// content tells a reader nothing. The id is excluded for the same reason: it
// names the pack, it is not part of what the pack says.
func ContentDigest(p Pack) string {
	var b strings.Builder

	// Trigger.
	b.WriteString("trigger|")
	b.WriteString(p.Spec.Trigger.Description)
	b.WriteString("|")
	b.WriteString(strings.Join(sortedCopy(p.Spec.Trigger.Keywords), ","))
	b.WriteString("\n")

	// Steps, in their recorded order: the order is the procedure, so it is part
	// of the content rather than something to normalise away.
	for _, s := range p.Spec.Steps {
		b.WriteString("step|")
		b.WriteString(s.Name)
		b.WriteString("|")
		b.WriteString(strings.Join(sortedCopy(s.Tools), ","))
		b.WriteString("\n")
	}

	// Constraints, sorted: two packs with the same constraints in a different
	// order impose the same rules.
	for _, c := range sortedConstraints(p.Spec.Constraints) {
		b.WriteString("constraint|")
		b.WriteString(c.DoNot)
		b.WriteString("|")
		b.WriteString(c.Because)
		b.WriteString("|")
		b.WriteString(c.Evidence)
		b.WriteString("\n")
	}

	b.WriteString("permissions|")
	b.WriteString(strings.Join(sortedCopy(p.Spec.ToolPermissions), ","))
	b.WriteString("\n")

	b.WriteString("eval|")
	b.WriteString(p.Spec.Eval.Suite)
	b.WriteString("|")
	b.WriteString(strings.Join(sortedCopy(p.Spec.Eval.Cases), ","))
	b.WriteString("\n")

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:8]
}

// --- merge helpers ---

// caseIDs lists the source cases, deduplicated and ordered.
func caseIDs(live []*Source) []string {
	seen := make(map[string]bool, len(live))
	out := make([]string, 0, len(live))
	for _, s := range live {
		id := strings.TrimSpace(s.Spec.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// runIDs lists the source runs.
func runIDs(live []*Source) []string {
	seen := make(map[string]bool, len(live))
	out := make([]string, 0, len(live))
	for _, s := range live {
		if s.RunID == "" || seen[s.RunID] {
			continue
		}
		seen[s.RunID] = true
		out = append(out, s.RunID)
	}
	sort.Strings(out)
	return out
}

// lessonIDs lists every lesson that contributed a constraint.
func lessonIDs(live []*Source) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range live {
		for _, l := range s.Lessons {
			if l.ID == "" || seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			out = append(out, l.ID)
		}
	}
	sort.Strings(out)
	return out
}

// mergedSteps unions the steps by name, keeping the order they were first seen
// in once the sources are ordered.
//
// A step's tools are unioned too: two cases reaching the same step through
// different tools both need their tool available.
func mergedSteps(live []*Source) []Step {
	var order []string
	tools := make(map[string]map[string]bool)

	for _, s := range live {
		for _, call := range s.ToolCalls {
			name := strings.TrimSpace(call.ToolName)
			if name == "" {
				continue
			}
			if _, seen := tools[name]; !seen {
				tools[name] = make(map[string]bool)
				order = append(order, name)
			}
			tools[name][name] = true
		}
	}

	out := make([]Step, 0, len(order))
	for _, name := range order {
		list := make([]string, 0, len(tools[name]))
		for t := range tools[name] {
			list = append(list, t)
		}
		sort.Strings(list)
		out = append(out, Step{Name: name, Tools: list})
	}
	return out
}

// mergedConstraints unions the constraints by their rule, keeping the first
// non-empty explanation and evidence for each.
//
// Union rather than intersection, because a constraint is a prohibition: taking
// the intersection would drop a rule one case needed in order to make two
// sources agree, which is discarding a safety property to tidy a document.
func mergedConstraints(live []*Source) []Constraint {
	byRule := make(map[string]*Constraint)
	var order []string

	for _, s := range live {
		for _, l := range s.Lessons {
			rule := strings.TrimSpace(l.Title)
			if rule == "" {
				continue
			}
			existing, seen := byRule[rule]
			if !seen {
				byRule[rule] = &Constraint{
					DoNot:    rule,
					Because:  strings.TrimSpace(l.Content),
					Evidence: l.ID,
				}
				order = append(order, rule)
				continue
			}
			// The same rule from another case: keep the first explanation (the
			// sources are ordered, so "first" is stable) and record the second
			// piece of evidence rather than losing it.
			if existing.Evidence != "" && l.ID != "" && !strings.Contains(existing.Evidence, l.ID) {
				existing.Evidence = existing.Evidence + "," + l.ID
			}
			if existing.Because == "" {
				existing.Because = strings.TrimSpace(l.Content)
			}
		}
	}

	out := make([]Constraint, 0, len(order))
	for _, rule := range order {
		out = append(out, *byRule[rule])
	}
	return out
}

// mergedPermissions unions the tool allow-list.
func mergedPermissions(live []*Source) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range live {
		for _, call := range s.ToolCalls {
			name := strings.TrimSpace(call.ToolName)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// mergedSuite picks the eval suite.
//
// An explicit one wins. Otherwise the sources' suites are compared, and when
// they disagree the first is taken — the disagreement is left to the readme and
// the review gate rather than resolved here, because choosing one case's suite
// silently would mean the merged skill is graded on half its origins.
func mergedSuite(live []*Source, explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if len(live) == 0 {
		return ""
	}
	return live[0].Spec.Suite
}

// mergedDescription composes the trigger description.
func mergedDescription(live []*Source, explicit string) string {
	if d := strings.TrimSpace(explicit); d != "" {
		return d
	}
	if len(live) == 1 {
		return live[0].Spec.Description
	}

	// Several cases: name them, so the trigger says what the skill was built
	// from rather than pretending it came from one of them.
	descriptions := make([]string, 0, len(live))
	for _, s := range live {
		if d := strings.TrimSpace(s.Spec.Description); d != "" {
			descriptions = append(descriptions, d)
		}
	}
	if len(descriptions) == 0 {
		return ""
	}
	return strings.Join(descriptions, "; ") + " (distilled from " +
		fmt.Sprintf("%d", len(live)) + " cases)"
}

// sortedCopy returns a sorted copy, leaving the input untouched.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// sortedConstraints orders constraints for digesting.
func sortedConstraints(in []Constraint) []Constraint {
	out := append([]Constraint(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DoNot != out[j].DoNot {
			return out[i].DoNot < out[j].DoNot
		}
		return out[i].Evidence < out[j].Evidence
	})
	return out
}
