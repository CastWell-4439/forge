package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Memory layers and reactive review (memory governance, part four).
//
// The lifecycle built so far answers "when does an entry stop competing for
// recall" and can only act on observed use. That turned out to be the wrong
// first question. Long-term memory has ONE shape today — a free-text summary
// written after every run, category "experience" — and a stream of
// per-run summaries does not accumulate knowledge, it accumulates sediment:
//
//   - each summary is about one situation, so it is neither a reusable rule nor
//     a durable fact;
//   - nothing relates a new summary to an existing one, so the same lesson
//     arrives ten times with ten different timestamps and ten different ids;
//   - the write gate asks "did this run do anything", not "is this worth
//     carrying forward".
//
// So the fix belongs one step earlier: separate what a memory IS, and give the
// ephemeral layer somewhere to go. Archiving cannot repair a store that is
// being filled with the wrong kind of thing.
//
//	FACT      a claim about the world that stays true until the world changes
//	          ("the project is maintained in Go"). Subject to verification.
//	EPISODIC  what one run saw and did. Worth keeping briefly, then either
//	          distilled into a skill or dropped — it is evidence of an event,
//	          not a rule.
//
// The third destination already exists: skillpack.Distill turns a verified run
// into a draft skill. Episodic memory is the raw material that distillation
// wants, which is why "condense or discard" is a choice rather than a threat.

// MemoryLayer says what kind of thing a memory is.
//
// The zero value is episodic, and that is a deliberate default: an unlabelled
// memory is treated as an observation about one run rather than as a claim
// about the world. Getting that backwards would let a single run's experience
// masquerade as a durable fact and survive verification it never faced.
type MemoryLayer string

const (
	// LayerFact is a claim about the world, subject to verification.
	LayerFact MemoryLayer = "fact"
	// LayerEpisodic is one run's experience: evidence of an event.
	LayerEpisodic MemoryLayer = "episodic"
)

// NormalizeMemoryLayer resolves a stored or configured layer.
//
// An unrecognised value becomes episodic rather than fact — see the type's
// comment. A typo must not promote an observation into a claim.
func NormalizeMemoryLayer(raw string) MemoryLayer {
	if MemoryLayer(raw) == LayerFact {
		return LayerFact
	}
	return LayerEpisodic
}

// DefaultEpisodicTTL is how long an episodic memory is worth keeping before the
// review proposes acting on it.
//
// It is a review threshold, not an expiry: crossing it produces a candidate for
// a human to look at, never a deletion.
const DefaultEpisodicTTL = 30 * 24 * time.Hour

// MemoryCandidateKind is what the review proposes to DO with an entry.
//
// Every kind is a PROPOSAL. Nothing in this file executes, and no exported
// function changes global state: the entries this produces are reviewed by a
// person, because memory is shared and its contents are model-generated.
type MemoryCandidateKind string

const (
	// CandidateDistill proposes turning an episodic memory into a skill draft.
	// The draft still has to pass skillpack's admission rules and a human's
	// review before it becomes a skill — a candidate is not a promotion.
	CandidateDistill MemoryCandidateKind = "distill"
	// CandidateDiscard proposes dropping an episodic memory as having no
	// reusable value.
	CandidateDiscard MemoryCandidateKind = "discard"
	// CandidatePromote proposes reclassifying an episodic memory as a fact.
	CandidatePromote MemoryCandidateKind = "promote"
	// CandidateVerify proposes checking a fact against the recorded evidence.
	CandidateVerify MemoryCandidateKind = "verify"
)

// MemoryCandidate is one proposed action, with its reasons.
type MemoryCandidate struct {
	EntryID string
	Kind    MemoryCandidateKind
	// Content is the entry's text, so a reviewer reads the proposal without a
	// second lookup.
	Content string
	// Reason explains the proposal in one line, always populated.
	Reason string
	// Confidence is how strongly the evidence supports the proposal, in 0..1.
	// It ranks the list for a reviewer; it does not trigger anything.
	Confidence float64
	// Evidence names what the proposal is based on (a run id, a tool call, an
	// agreement count), so a reviewer can check it.
	Evidence string
}

// Assertion is a claim about the world extracted from a memory's text.
//
// Extraction exists so a fact can be checked against what the runs actually
// did. It is deliberately narrow in what it reads and explicit about where each
// value came from: a checker that cannot say WHY it thinks a memory is wrong is
// worse than no checker, because a reviewer cannot audit it.
type Assertion struct {
	// Kind is the assertion's category: "language", "path", "version",
	// "service", "count".
	Kind string
	// Value is the extracted value, lowercased and trimmed.
	Value string
	// Source is how it was found: "rule" for the static pass, "model" for the
	// LLM pass. A reviewer must be able to tell a deterministic finding from a
	// suggested one.
	Source string
	// Span is the matched text as it appeared, for display.
	Span string
}

// ExtractionSource names where an assertion came from.
const (
	// ExtractionRule is the static, deterministic pass. It runs always, needs
	// no model, and is the fallback whenever the model is unavailable.
	ExtractionRule = "rule"
	// ExtractionModel is the model-assisted pass. It finds more, and every
	// finding it produces is marked as such.
	ExtractionModel = "model"
)

// staticLanguageNames is the vocabulary the rule pass recognises.
//
// A closed list rather than a heuristic: "is this token a programming
// language?" cannot be answered by shape, and a guess would produce assertions
// nobody can trust. A language outside this list is simply not extracted by the
// rule pass — the model pass may still find it, and its finding says so.
var staticLanguageNames = []string{
	"python", "go", "golang", "java", "javascript", "typescript", "rust", "c++", "cpp",
	"c#", "csharp", "ruby", "php", "swift", "kotlin", "scala", "perl", "bash", "shell",
	"sql", "terraform", "hcl", "yaml", "dockerfile",
}

// ExtractAssertionsStatic is the deterministic extraction pass.
//
// It recognises a small, closed vocabulary of claims that operational memory
// actually makes — the languages, paths, versions and services a project is
// described by — and reports each with its kind. It never guesses: a token that
// is not in a known list is not an assertion, because an invented assertion
// would make the whole verification path untrustworthy.
//
// It runs unconditionally, including when a model is configured. That is the
// point of having it: the model pass adds findings, and the static pass is what
// remains when the model is absent, misconfigured or wrong.
func ExtractAssertionsStatic(text string) []Assertion {
	lowered := strings.ToLower(text)
	var out []Assertion
	seen := map[string]bool{}

	add := func(kind, value, span string) {
		key := kind + "\x00" + value
		if value == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Assertion{Kind: kind, Value: value, Source: ExtractionRule, Span: span})
	}

	// Languages: whole-word matches against the closed list.
	for _, lang := range staticLanguageNames {
		if idx := indexWord(lowered, lang); idx >= 0 {
			add("language", lang, text[idx:idx+len(lang)])
		}
	}

	// Paths: tokens that look like file paths with a known extension. The
	// extension is what makes it a path rather than a slash.
	for _, field := range strings.Fields(text) {
		cleaned := strings.Trim(field, ".,;:!?\"'()`")
		if ext, ok := pathExtension(cleaned); ok {
			add("path", strings.ToLower(ext), cleaned)
		}
	}

	// Versions: a leading v or a bare x.y[.z] against a version-ish word.
	for _, field := range strings.Fields(text) {
		cleaned := strings.Trim(field, ".,;:!?\"'()`")
		if isVersionToken(cleaned) {
			add("version", strings.ToLower(cleaned), cleaned)
		}
	}

	// Services: a host:port or a known service word.
	for _, field := range strings.Fields(text) {
		cleaned := strings.Trim(field, ".,;:!?\"'()`")
		if isServiceToken(cleaned) {
			add("service", strings.ToLower(cleaned), cleaned)
		}
	}

	return out
}

// indexWord finds needle in haystack at a word boundary.
//
// Substring matching would make "go" match "going" and "django" — the same
// mistake the tool-relevance ranking made and had to fix. A language name is a
// word or it is nothing.
func indexWord(haystack, needle string) int {
	from := 0
	for {
		idx := strings.Index(haystack[from:], needle)
		if idx < 0 {
			return -1
		}
		start := from + idx
		end := start + len(needle)
		leftOK := start == 0 || !isWordByte(haystack[start-1])
		rightOK := end == len(haystack) || !isWordByte(haystack[end])
		if leftOK && rightOK {
			return start
		}
		from = start + 1
		if from >= len(haystack) {
			return -1
		}
	}
}

// isWordByte reports whether b can be part of a word for boundary purposes.
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '+'
}

// PathExtensionKnown reports whether an extension names a source or config
// file, so callers outside this package can apply the same rule.
//
// Exported because the evidence projector (which lives in the other plane) has
// to decide whether a value inside a tool call's free-form payload is a path,
// and it must decide it the same way the assertion extractor does. Two
// implementations of "is this a path" would drift, and the drift would show up
// as a verification pass finding evidence the extractor never produces.
func PathExtensionKnown(ext string) (string, bool) {
	return pathExtension("x." + strings.TrimPrefix(strings.ToLower(ext), "."))
}

// pathExtension reports whether a token looks like a file path, returning its
// extension.
//
// The rule: a known source/config extension, either bare ("main.go") or inside
// a path ("internal/agent/core/tools.go"). An unknown extension is not a path —
// "1.5" and "v2.0" must not be read as file names.
func pathExtension(token string) (string, bool) {
	idx := strings.LastIndexByte(token, '.')
	if idx <= 0 || idx == len(token)-1 {
		return "", false
	}
	ext := strings.ToLower(token[idx+1:])
	switch ext {
	case "go", "py", "js", "ts", "tsx", "jsx", "java", "rs", "rb", "php", "cs", "kt", "swift",
		"c", "h", "cpp", "hpp", "cc", "sql", "yaml", "yml", "json", "toml", "ini", "md",
		"sh", "ps1", "dockerfile", "tf", "proto":
		return ext, true
	default:
		return "", false
	}
}

// isVersionToken reports whether a token is a version number.
//
// It requires either an explicit v prefix or a digit-dot-digit shape, so
// "100rps" and "2026-01-01" are not versions while "v2", "1.5" and "3.11.2" are.
func isVersionToken(token string) bool {
	if len(token) < 2 {
		return false
	}
	body := token
	if body[0] == 'v' || body[0] == 'V' {
		body = body[1:]
		if body == "" {
			return false
		}
	}
	dots, digits, others := 0, 0, 0
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
		default:
			others++
		}
	}
	if others > 0 || digits == 0 {
		return false
	}
	// A bare number is a count, not a version; a version needs a dot or a v.
	return dots > 0 || token[0] == 'v' || token[0] == 'V'
}

// isServiceToken reports whether a token names a service.
//
// The shape is host:port or a hostname with a dot, which is how deployments are
// described. A bare word is not a service: "production" alone says nothing a
// checker can use.
func isServiceToken(token string) bool {
	if strings.Contains(token, "://") {
		return true
	}
	if _, port, ok := strings.Cut(token, ":"); ok {
		if port == "" {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	// A dotted hostname. Requires letters on both sides of a dot, so "1.5" and
	// "main.go" do not qualify.
	if idx := strings.LastIndexByte(token, '.'); idx > 0 && idx < len(token)-1 {
		left := token[:idx]
		hasLetter := false
		for _, r := range left {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				hasLetter = true
				break
			}
		}
		if !hasLetter {
			return false
		}
		if _, isPath := pathExtension(token); isPath {
			return false
		}
		return true
	}
	return false
}

// MergeAssertions combines the rule and model passes, preferring the rule pass
// on a duplicate and recording each finding's origin.
//
// The rule pass wins ties because it is reproducible: when both passes find the
// same value, the finding a reviewer can re-derive by hand is the more useful
// one to attribute it to.
func MergeAssertions(rule, model []Assertion) []Assertion {
	out := make([]Assertion, 0, len(rule)+len(model))
	seen := map[string]bool{}
	for _, a := range rule {
		key := a.Kind + "\x00" + a.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	for _, a := range model {
		key := a.Kind + "\x00" + a.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	// Deterministic order: a review list that reorders between runs is hard to
	// compare, and comparing two reviews is how a reviewer spots a trend.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// AssertionExtractor is the model-assisted pass, injected.
//
// It is an interface rather than a call into a client because the review runs
// in the control plane, which does not import the agent's LLM client (the
// planes do not import each other — see the lifecycle's observation sink for
// the same pattern). A deployment with no model configured simply has no
// extractor, and the static pass carries on alone.
//
// The signature is narrow on purpose: text in, assertions out. An extractor
// that could act would make "review only proposes" unenforceable.
type AssertionExtractor interface {
	// ExtractAssertions returns claims found in text, each marked
	// ExtractionModel. An error means the pass produced nothing usable; the
	// caller falls back to the static pass alone and does not fail the review.
	ExtractAssertions(ctx context.Context, text string) ([]Assertion, error)
}

// SortedCandidates orders proposals for review: strongest first, then by id so
// repeated reviews agree.
func SortedCandidates(candidates []MemoryCandidate) []MemoryCandidate {
	out := append([]MemoryCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].EntryID < out[j].EntryID
	})
	return out
}
