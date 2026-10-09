package skillpack

import (
	"sort"
	"strings"
	"time"
)

// Batch regression checking: running every skill's bound cases.
//
// `skills verify` already answered the question for ONE skill — it executes the
// cases and reads the verdict, which is the hard part and was already done. What
// it could not do is answer it for the SET: an operator asking "has anything
// regressed" had to name each skill, and the answer evaporated when the command
// exited.
//
// This file supplies the two missing pieces, and neither of them is execution:
//
//	SELECTION  which skills are worth running, and in what order
//	RECORDING  what the outcome was, so the next reader does not re-run
//
// The execution itself is injected (CaseRunner), because it lives in the
// command layer where the eval configuration and the run artifacts are known.
// Keeping it out of this package is what makes the batching testable without
// executing scenarios.

// CaseRunner executes one skill's bound cases and reports which failed.
//
// An error means the check could not be completed — a missing case, an
// unreadable config — and is reported differently from a failing case: "I could
// not tell you" is not the same answer as "it broke", and conflating them would
// let a broken harness look like a broken skill.
type CaseRunner func(pack Pack) (failed []string, err error)

// CheckResult is one skill's outcome in a batch run.
type CheckResult struct {
	Pack Pack
	// FailedCases are the bound cases that no longer matched, empty on success.
	FailedCases []string
	// Err is a check that could not be completed. It is reported, never
	// recorded as a regression.
	Err error
	// Skipped is why the skill was not run, empty when it was.
	Skipped string
	// Recorded reports whether the outcome was written onto the skill.
	Recorded bool
}

// Passed reports whether the skill's cases all still matched.
func (r CheckResult) Passed() bool { return r.Err == nil && r.Skipped == "" && len(r.FailedCases) == 0 }

// CheckConfig tunes a batch run.
type CheckConfig struct {
	// IncludeDeprecated runs retired skills too. Off by default: a retired
	// skill is out of use, and checking it would ask someone to fix something
	// nobody runs.
	IncludeDeprecated bool
	// Max caps how many skills are checked, so a large store does not turn one
	// command into an unbounded amount of work.
	Max int
	// Record writes each outcome onto the skill. Off means "check and report",
	// which is the right default for a dry run and for a store under version
	// control where the operator may not want a diff.
	Record bool
	// Now is the reference time, injected for deterministic tests.
	Now time.Time
}

func (c CheckConfig) normalize() CheckConfig {
	if c.Now.IsZero() {
		c.Now = time.Now().UTC()
	}
	return c
}

// CheckSkills runs the bound cases for a set of skills and optionally records
// the outcomes.
//
// The order is deliberate: skills whose LAST check failed come first. A batch
// run answers "did anything regress", and a skill that regressed on the
// previous run and has not been dealt with since is the most likely answer —
// putting it at the top means the report opens with the most probable news.
func CheckSkills(store Store, runner CaseRunner, cfg CheckConfig) ([]CheckResult, error) {
	cfg = cfg.normalize()

	all, err := store.ListAll()
	if err != nil {
		return nil, err
	}

	candidates := make([]Pack, 0, len(all))
	for _, p := range all {
		// A draft has never been in use, so there is no regression to detect:
		// running its cases would report on something nobody can apply yet.
		if p.Metadata.Status == StatusDraft {
			continue
		}
		if p.Metadata.IsDeprecated() && !cfg.IncludeDeprecated {
			continue
		}
		candidates = append(candidates, p)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		// Previously failing first, then by id so a batch run is reproducible.
		pi, pj := candidates[i].Metadata.NeedsAttention(), candidates[j].Metadata.NeedsAttention()
		if pi != pj {
			return pi
		}
		return candidates[i].Metadata.ID < candidates[j].Metadata.ID
	})

	if cfg.Max > 0 && len(candidates) > cfg.Max {
		candidates = candidates[:cfg.Max]
	}

	results := make([]CheckResult, 0, len(candidates))
	for _, pack := range candidates {
		result := CheckResult{Pack: pack}

		// A skill with no bound cases cannot be checked, and saying so is more
		// useful than reporting it as passing: an empty check is not evidence
		// that the skill still works.
		if len(pack.Spec.Eval.Cases) == 0 {
			result.Skipped = "no bound cases to run"
			results = append(results, result)
			continue
		}

		failed, err := runner(pack)
		if err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		result.FailedCases = failed

		if cfg.Record {
			updated, rerr := store.RecordVerification(pack.Metadata.ID, failed, cfg.Now)
			if rerr != nil {
				// The check succeeded but the note failed. Reporting the check
				// as an error would misattribute a write problem to the skill.
				result.Err = rerr
				results = append(results, result)
				continue
			}
			result.Pack = updated
			result.Recorded = true
		}
		results = append(results, result)
	}
	return results, nil
}

// Summary counts the outcomes for a report header.
type Summary struct {
	Checked  int
	Passed   int
	Failed   int
	Errored  int
	Skipped  int
	Recorded int
}

// Summarize counts a batch's outcomes.
func Summarize(results []CheckResult) Summary {
	var s Summary
	for _, r := range results {
		s.Checked++
		switch {
		case r.Skipped != "":
			s.Skipped++
		case r.Err != nil:
			s.Errored++
		case len(r.FailedCases) == 0:
			s.Passed++
		default:
			s.Failed++
		}
		if r.Recorded {
			s.Recorded++
		}
	}
	return s
}

// FailedIDs lists the skills that regressed, for a one-line report.
func FailedIDs(results []CheckResult) []string {
	var out []string
	for _, r := range results {
		if r.Err == nil && r.Skipped == "" && len(r.FailedCases) > 0 {
			out = append(out, r.Pack.Metadata.ID)
		}
	}
	return out
}

// FormatFailedCases renders a skill's failing cases for display.
func FormatFailedCases(cases []string) string {
	if len(cases) == 0 {
		return "(none)"
	}
	return strings.Join(cases, ", ")
}
