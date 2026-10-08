package core

import (
	"sort"
	"strings"
	"time"
)

// Verification: checking a fact against what the runs actually did.
//
// The review (part four) proposes checking facts but cannot check them: it
// reads the memory, not the evidence. This file closes that by reading the
// recorded tool calls — the one place where "what the project actually is"
// shows up as data rather than as a claim.
//
// Why this works where the archive could not: the archive needed a usage
// signal, which no run can honestly produce. Verification compares two things
// that both exist — what a memory asserts, and what the tools touched — so it
// needs nothing new to be recorded. The evidence was already there and simply
// had no reader.
//
// The rule that shapes everything here: REPORT THE DISAGREEMENT, NOT A VERDICT.
// A memory saying "the project is written in Go" and recent runs touching only
// .py files is a conflict. Which side is wrong is NOT knowable from this
// evidence — the last twenty runs may have touched one subsystem — so the
// output names the conflict and shows the evidence, and a person decides. This
// is the same rule as recall's "present both sides of a conflict rather than
// choosing" (see evidence.go), applied to facts rather than to recall.

// EvidenceToolCall is one recorded tool invocation, as verification reads it.
//
// It is a projection of the runtime's model.ToolCall into the agent plane's
// vocabulary, so the control plane can build it and the core can judge it
// without either importing the other's types.
type EvidenceToolCall struct {
	RunID    string
	ToolName string
	// Paths are the file paths this call touched, taken from its arguments and
	// results. They are extracted by the builder, which knows the shape of a
	// tool call; the judge only needs the paths.
	Paths []string
	// Text is the call's free-form content (arguments and result values
	// flattened), used to look for values a memory claims.
	Text string
	// Failed reports whether the call errored.
	Failed bool
	// Error is the failure message, when there was one.
	Error string
	At    time.Time
}

// VerificationConfig tunes what counts as a disagreement.
type VerificationConfig struct {
	// MinEvidence is how many observations of the OPPOSITE value are needed
	// before a language conflict is reported.
	//
	// A single counterexample is not evidence: one README edit in a Go project
	// does not make it a Markdown project. The threshold is what keeps the
	// report from being noise.
	MinEvidence int
	// MaxCalls caps how many tool calls are examined, newest first. Verification
	// asks about the CURRENT world, so old evidence is worse than no evidence.
	MaxCalls int
	// MinVersionEvidence is how many times the evidence must show a different
	// version of the claimed software before it counts. Text evidence is weaker
	// than a file extension, so it takes more of it.
	MinVersionEvidence int
	// MinServiceAttempts is how many failed attempts at the claimed service are
	// needed. It is separate from the language threshold because a single
	// connection failure is as likely to be a transient network problem as a
	// wrong memory.
	MinServiceAttempts int
	// Now is the reference time, injected for deterministic tests.
	Now time.Time
}

// DefaultVerificationConfig returns the standard thresholds.
func DefaultVerificationConfig() VerificationConfig {
	return VerificationConfig{
		MinEvidence:        5,
		MaxCalls:           500,
		MinVersionEvidence: 2,
		MinServiceAttempts: 2,
	}
}

func (c VerificationConfig) normalize() VerificationConfig {
	d := DefaultVerificationConfig()
	if c.MinEvidence <= 0 {
		c.MinEvidence = d.MinEvidence
	}
	if c.MaxCalls <= 0 {
		c.MaxCalls = d.MaxCalls
	}
	if c.MinVersionEvidence <= 0 {
		c.MinVersionEvidence = d.MinVersionEvidence
	}
	if c.MinServiceAttempts <= 0 {
		c.MinServiceAttempts = d.MinServiceAttempts
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	return c
}

// Conflict is one disagreement between a memory and the evidence.
type Conflict struct {
	// EntryID is the memory the claim came from.
	EntryID string
	// Kind is the assertion kind that disagreed: "language", "path", ...
	Kind string
	// Claimed is what the memory says.
	Claimed string
	// Observed is what the evidence shows instead.
	Observed string
	// Evidence describes where the observation came from, in a form a reviewer
	// can check by hand: run id, tool name, count.
	Evidence string
	// Count is how many observations supported the observed value.
	Count int
	// Confidence ranks conflicts for a reviewer; it triggers nothing.
	Confidence float64
}

// VerificationResult is everything a verification pass found.
type VerificationResult struct {
	Conflicts []Conflict
	// CallsExamined is how much evidence was read, so a reviewer can tell "no
	// conflicts" from "no evidence".
	CallsExamined int
	// FactsChecked is how many memories carried a checkable claim.
	FactsChecked int
}

// VerifyClaims checks a set of asserted claims against recorded evidence.
//
// claims maps an entry id to the assertions extracted from it (by the review's
// extraction passes). The result reports disagreements only: an agreement is
// not news, and a claim with no relevant evidence is not a finding — it is the
// absence of one, which is why calls are counted separately in the result.
func VerifyClaims(claims map[string][]Assertion, calls []EvidenceToolCall, cfg VerificationConfig) VerificationResult {
	cfg = cfg.normalize()

	// Newest first, and capped: verification asks about the current world.
	ordered := append([]EvidenceToolCall(nil), calls...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.After(ordered[j].At) })
	if len(ordered) > cfg.MaxCalls {
		ordered = ordered[:cfg.MaxCalls]
	}

	var result VerificationResult
	result.CallsExamined = len(ordered)

	// Index the evidence once, then answer every claim from it.
	evidence := indexEvidence(ordered)

	ids := make([]string, 0, len(claims))
	for id := range claims {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic output

	for _, id := range ids {
		for _, assertion := range claims[id] {
			result.FactsChecked++
			if conflict, ok := checkAssertion(id, assertion, evidence, cfg); ok {
				result.Conflicts = append(result.Conflicts, conflict)
			}
		}
	}

	result.Conflicts = SortedConflicts(result.Conflicts)
	return result
}

// evidenceIndex is the evidence arranged for lookup by assertion kind.
type evidenceIndex struct {
	// languageCounts is how many path observations carried each extension.
	languageCounts map[string]int
	// languageRuns records one run id per language, for the evidence line.
	languageRuns map[string]string
	// pathAttempts and pathSuccesses count how each claimed path fared.
	pathAttempts  map[string]int
	pathSuccesses map[string]int
	// pathRuns records one run id per path, for the evidence line.
	pathRuns map[string]string
	// pathError is one error message per path, to show what went wrong.
	pathError map[string]string
	// versionsBySubject maps a software name to the versions seen beside it in
	// tool output. Pairing is what makes a version checkable: a bare number
	// could belong to anything.
	versionsBySubject map[string]map[string]int
	// versionRuns records a run id per subject:version pair.
	versionRuns map[string]string
	// serviceAttempts and serviceSuccesses count how each host fared.
	serviceAttempts  map[string]int
	serviceSuccesses map[string]int
	// serviceRuns records a run id per service, and serviceError one message.
	serviceRuns  map[string]string
	serviceError map[string]string
}

// indexEvidence builds the lookup tables in one pass over the calls.
//
// Every "which run witnessed this" record keeps the NEWEST witness. That
// matters because the evidence line in a report is what a reviewer follows to
// check the finding, and a stale run id sends them to the wrong place. An
// earlier version recorded the first call it saw, which was correct only as a
// side effect of the caller's ordering — the sort that happened to put the
// newest first. A test that passed locally on that accident failed on CI, which
// is the useful version of this lesson.
func indexEvidence(calls []EvidenceToolCall) evidenceIndex {
	idx := evidenceIndex{
		languageCounts:    map[string]int{},
		languageRuns:      map[string]string{},
		pathAttempts:      map[string]int{},
		pathSuccesses:     map[string]int{},
		pathRuns:          map[string]string{},
		pathError:         map[string]string{},
		versionsBySubject: map[string]map[string]int{},
		versionRuns:       map[string]string{},
		serviceAttempts:   map[string]int{},
		serviceSuccesses:  map[string]int{},
		serviceRuns:       map[string]string{},
		serviceError:      map[string]string{},
	}

	languageAt := map[string]time.Time{}
	pathAt := map[string]time.Time{}
	versionAt := map[string]time.Time{}
	serviceAt := map[string]time.Time{}

	for _, call := range calls {
		// Version and service evidence comes from the call's flattened text,
		// because a version or a hostname can appear anywhere in a worker's
		// payload and there is no field to read it from.
		for _, pair := range versionPairs(call.Text) {
			if idx.versionsBySubject[pair.Subject] == nil {
				idx.versionsBySubject[pair.Subject] = map[string]int{}
			}
			idx.versionsBySubject[pair.Subject][pair.Version]++
			key := pair.Subject + ":" + pair.Version
			if prev, seen := versionAt[key]; !seen || call.At.After(prev) {
				versionAt[key] = call.At
				idx.versionRuns[key] = call.RunID
			}
		}

		for _, svc := range serviceTokens(call.Text) {
			idx.serviceAttempts[svc]++
			if !call.Failed {
				idx.serviceSuccesses[svc]++
			} else if _, seen := idx.serviceError[svc]; !seen {
				idx.serviceError[svc] = call.Error
			}
			if prev, seen := serviceAt[svc]; !seen || call.At.After(prev) {
				serviceAt[svc] = call.At
				idx.serviceRuns[svc] = call.RunID
			}
		}

		for _, path := range call.Paths {
			lowered := strings.ToLower(path)

			// Language evidence: the file extension a call touched.
			if ext, ok := pathExtension(lowered); ok {
				if lang, known := languageForExtension(ext); known {
					idx.languageCounts[lang]++
					if prev, seen := languageAt[lang]; !seen || call.At.After(prev) {
						languageAt[lang] = call.At
						idx.languageRuns[lang] = call.RunID
					}
				}
			}

			// Path evidence: was this path touched, and did it work?
			idx.pathAttempts[lowered]++
			if !call.Failed {
				idx.pathSuccesses[lowered]++
			} else if _, seen := idx.pathError[lowered]; !seen {
				// The error message is for display; the newest attempt's wording
				// is the most likely to match what a reviewer would see now.
				idx.pathError[lowered] = call.Error
			}
			if prev, seen := pathAt[lowered]; !seen || call.At.After(prev) {
				pathAt[lowered] = call.At
				idx.pathRuns[lowered] = call.RunID
			}
		}
	}
	return idx
}

// versionPair is one software-version pairing found in evidence text.
type versionPair struct {
	Subject string
	Version string
}

// versionPairs finds "software version" pairs in free text.
//
// The scan mirrors the extractor's: a version token whose preceding word is a
// known software name. Running the SAME rule on both sides is what makes the
// comparison meaningful — if the extractor attached a subject by one rule and
// the evidence indexer used another, the two would disagree about what a
// version belongs to, and every comparison would be between unrelated things.
func versionPairs(text string) []versionPair {
	fields := strings.Fields(text)
	var out []versionPair
	for i, field := range fields {
		cleaned := strings.Trim(field, ".,;:!?\"'()`[]{}")
		if !isVersionToken(cleaned) {
			continue
		}
		if subject := versionSubject(fields, i); subject != "" {
			out = append(out, versionPair{Subject: subject, Version: strings.ToLower(cleaned)})
		}
	}
	return out
}

// serviceTokens finds host-shaped tokens in free text.
//
// A token is a service if it has a port, a scheme, or a dotted hostname shape.
// The rule is the extractor's, for the same reason versionPairs uses the
// extractor's.
func serviceTokens(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(text) {
		cleaned := strings.Trim(field, ".,;:!?\"'()`[]{}")
		if !isServiceToken(cleaned) {
			continue
		}
		lowered := strings.ToLower(cleaned)
		if seen[lowered] {
			continue
		}
		seen[lowered] = true
		out = append(out, lowered)
	}
	return out
}

// checkAssertion decides whether one claim disagrees with the evidence.
//
// It returns no conflict when the evidence is silent or agrees. Both outcomes
// are ordinary: a claim nobody has tested is not a finding, and reporting it
// would bury the real disagreements in noise.
func checkAssertion(entryID string, assertion Assertion, idx evidenceIndex, cfg VerificationConfig) (Conflict, bool) {
	switch assertion.Kind {
	case "language":
		return checkLanguage(entryID, assertion, idx, cfg)
	case "path":
		return checkPath(entryID, assertion, idx, cfg)
	case "version":
		return checkVersion(entryID, assertion, idx, cfg)
	case "service":
		return checkService(entryID, assertion, idx, cfg)
	default:
		// A claim kind with no rule is unanswered, not a disagreement.
		return Conflict{}, false
	}
}

// checkVersion reports when the evidence shows a DIFFERENT version of the same
// software.
//
// Both halves matter. Comparing version numbers without a subject would flag
// "v2.1" against any version anywhere in the logs, which is noise; requiring
// the subject to appear next to a different version asks the only answerable
// question. An assertion with no subject is skipped rather than guessed at —
// the extractor refused to invent one, and verification must not undo that.
func checkVersion(entryID string, assertion Assertion, idx evidenceIndex, cfg VerificationConfig) (Conflict, bool) {
	subject := assertion.Subject
	if subject == "" {
		return Conflict{}, false
	}
	claimed := assertion.Value

	observed := idx.versionsBySubject[subject]
	if len(observed) == 0 {
		return Conflict{}, false
	}

	best, bestCount := "", 0
	for version, count := range observed {
		if version == claimed {
			// The claimed version IS present: the memory agrees with the
			// evidence, whatever else also appears.
			return Conflict{}, false
		}
		if count > bestCount || (count == bestCount && version < best) {
			best, bestCount = version, count
		}
	}
	if bestCount < cfg.MinVersionEvidence {
		return Conflict{}, false
	}

	return Conflict{
		EntryID:    entryID,
		Kind:       "version",
		Claimed:    subject + " " + claimed,
		Observed:   subject + " " + best,
		Evidence:   "run " + idx.versionRuns[subject+":"+best] + ", " + itoa(bestCount) + " occurrence(s) in tool output",
		Count:      bestCount,
		Confidence: versionConfidence(bestCount, cfg.MinVersionEvidence),
	}, true
}

// versionConfidence stays below the language ceiling: a version seen in free
// text is weaker evidence than a file extension, because the text may describe
// something other than the project's own toolchain.
func versionConfidence(count, minEvidence int) float64 {
	conf := 0.5 + 0.05*float64(count-minEvidence)
	if conf > 0.7 {
		conf = 0.7
	}
	if conf < 0.5 {
		conf = 0.5
	}
	return conf
}

// checkService reports when the evidence tried the claimed service and always
// failed.
//
// The shape mirrors the path rule, and for the same reason: "the evidence never
// mentions this service" would flag almost every service claim, because a
// project connects to many things and its runs touch few of them. An attempt
// that failed is different — something reached for the service the memory
// names, and the world refused.
//
// What it deliberately does NOT do is compare service NAMES. A project using
// several services is ordinary, so "the evidence shows a different host" is not
// a disagreement; only unreachability is.
func checkService(entryID string, assertion Assertion, idx evidenceIndex, cfg VerificationConfig) (Conflict, bool) {
	claimed := strings.ToLower(assertion.Value)

	attempts, ok := idx.serviceAttempts[claimed]
	if !ok || attempts == 0 {
		return Conflict{}, false
	}
	if idx.serviceSuccesses[claimed] > 0 {
		// It answered at least once, so the service exists.
		return Conflict{}, false
	}
	if attempts < cfg.MinServiceAttempts {
		// A single failure is as likely to be a transient network problem as a
		// wrong memory, so the threshold is separate from the language one.
		return Conflict{}, false
	}

	observed := "unreachable"
	if msg := idx.serviceError[claimed]; msg != "" {
		observed = "always failed: " + truncate(msg, 80)
	}
	conf := 0.5 + 0.05*float64(attempts-cfg.MinServiceAttempts)
	if conf > 0.7 {
		conf = 0.7
	}

	return Conflict{
		EntryID:    entryID,
		Kind:       "service",
		Claimed:    assertion.Value,
		Observed:   observed,
		Evidence:   "run " + idx.serviceRuns[claimed] + ", " + itoa(attempts) + " failed attempt(s), 0 successes",
		Count:      attempts,
		Confidence: conf,
	}, true
}

// checkLanguage reports when the project's files disagree with the claimed
// language.
//
// The comparison is between the CLAIMED language and every OTHER language the
// evidence shows. It reports the strongest alternative, and only when that
// alternative is at least the threshold: one stray file is not a rebuttal.
func checkLanguage(entryID string, assertion Assertion, idx evidenceIndex, cfg VerificationConfig) (Conflict, bool) {
	claimed := assertion.Value

	bestLang, bestCount := "", 0
	for lang, count := range idx.languageCounts {
		if lang == claimed {
			continue
		}
		if count > bestCount || (count == bestCount && lang < bestLang) {
			bestLang, bestCount = lang, count
		}
	}

	if bestCount < cfg.MinEvidence {
		return Conflict{}, false
	}
	// A language the claim names that is ALSO well represented is not a
	// disagreement: a project can legitimately contain two.
	if idx.languageCounts[claimed] >= bestCount {
		return Conflict{}, false
	}

	return Conflict{
		EntryID:    entryID,
		Kind:       "language",
		Claimed:    claimed,
		Observed:   bestLang,
		Evidence:   "run " + idx.languageRuns[bestLang] + ", " + itoa(bestCount) + " file(s) touched",
		Count:      bestCount,
		Confidence: languageConfidence(idx.languageCounts[claimed], bestCount),
	}, true
}

// languageConfidence scales with how lopsided the evidence is.
//
// It is capped below certainty on purpose: the evidence is a sample of what the
// runs happened to touch, not a census of the repository, and a number near 1.0
// would invite a reader to treat it as proof.
func languageConfidence(claimedCount, observedCount int) float64 {
	total := claimedCount + observedCount
	if total == 0 {
		return 0
	}
	share := float64(observedCount) / float64(total)
	// Map share into [0.4, 0.9]: even a lopsided sample is a sample.
	conf := 0.4 + 0.5*share
	if conf > 0.9 {
		conf = 0.9
	}
	return conf
}

// checkPath reports when a claimed path was tried and never worked.
//
// The rule is "attempted and always failed", not "never seen". Most paths in a
// project are never touched by any recorded call, so "never seen" would report
// almost every path claim and drown the real finding. An attempt that failed is
// different: something tried to use the path the memory names, and the world
// said no.
func checkPath(entryID string, assertion Assertion, idx evidenceIndex, cfg VerificationConfig) (Conflict, bool) {
	claimed := strings.ToLower(assertion.Value)

	attempts, ok := idx.pathAttempts[claimed]
	if !ok || attempts == 0 {
		// Never touched: no evidence either way.
		return Conflict{}, false
	}
	if idx.pathSuccesses[claimed] > 0 {
		// It worked at least once, so the path exists. A later failure is a
		// different problem than the memory being wrong.
		return Conflict{}, false
	}

	observed := "missing or unreadable"
	if msg := idx.pathError[claimed]; msg != "" {
		observed = "always failed: " + truncate(msg, 80)
	}

	conf := 0.5 + 0.05*float64(attempts)
	if conf > 0.9 {
		conf = 0.9
	}

	return Conflict{
		EntryID:    entryID,
		Kind:       "path",
		Claimed:    assertion.Value,
		Observed:   observed,
		Evidence:   "run " + idx.pathRuns[claimed] + ", " + itoa(attempts) + " failed attempt(s), 0 successes",
		Count:      attempts,
		Confidence: conf,
	}, true
}

// SortedConflicts orders conflicts for review: strongest first, then by entry
// and kind so repeated reviews agree.
func SortedConflicts(conflicts []Conflict) []Conflict {
	out := append([]Conflict(nil), conflicts...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		if out[i].EntryID != out[j].EntryID {
			return out[i].EntryID < out[j].EntryID
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// languageForExtension maps a file extension to the language a memory would
// name it by.
//
// Only extensions with an unambiguous language are mapped: ".md" and ".json"
// are formats, not languages, and reporting "the project is Markdown" would be
// nonsense. An unmapped extension contributes no language evidence at all.
func languageForExtension(ext string) (string, bool) {
	switch ext {
	case "go":
		return "go", true
	case "py":
		return "python", true
	case "js", "jsx", "mjs", "cjs":
		return "javascript", true
	case "ts", "tsx":
		return "typescript", true
	case "java":
		return "java", true
	case "rs":
		return "rust", true
	case "rb":
		return "ruby", true
	case "php":
		return "php", true
	case "cs":
		return "c#", true
	case "kt", "kts":
		return "kotlin", true
	case "swift":
		return "swift", true
	case "c", "h":
		return "c", true
	case "cpp", "cc", "hpp":
		return "c++", true
	case "scala":
		return "scala", true
	case "sh", "bash":
		return "bash", true
	case "sql":
		return "sql", true
	default:
		return "", false
	}
}

// truncate shortens s to n bytes at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}
