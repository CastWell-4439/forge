package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// call builds an evidence call.
func call(runID, tool string, paths []string, failed bool, errMsg string) EvidenceToolCall {
	return EvidenceToolCall{
		RunID: runID, ToolName: tool, Paths: paths,
		Failed: failed, Error: errMsg, At: time.Now(),
	}
}

// --- language conflicts ---

// A memory claiming one language while the files touched are overwhelmingly
// another is a disagreement worth reporting.
func TestLanguageConflictReported(t *testing.T) {
	var calls []EvidenceToolCall
	for i := 0; i < 8; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"main.py"}, false, ""))
	}
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go", Source: ExtractionRule}},
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())

	require.Len(t, result.Conflicts, 1)
	c := result.Conflicts[0]
	assert.Equal(t, "language", c.Kind)
	assert.Equal(t, "go", c.Claimed)
	assert.Equal(t, "python", c.Observed)
	assert.Contains(t, c.Evidence, "run-1", "the evidence names where it came from")
	assert.Contains(t, c.Evidence, "8", "and how much of it there was")
	assert.Equal(t, 1, result.FactsChecked)
	assert.Equal(t, 8, result.CallsExamined)
}

// A single counterexample is not a rebuttal: one README edit in a Go project
// does not make it a Markdown project. This threshold is what keeps the report
// from being noise.
func TestLanguageConflictNeedsEnoughEvidence(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}

	// Two python files against a claimed Go project: below the default of 5.
	calls := []EvidenceToolCall{
		call("run-1", "file.read", []string{"a.py"}, false, ""),
		call("run-1", "file.read", []string{"b.py"}, false, ""),
	}
	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "two files is a sample, not a rebuttal")

	// The threshold is configurable, so a deployment can ask for more or fewer.
	cfg := DefaultVerificationConfig()
	cfg.MinEvidence = 2
	result = VerifyClaims(claims, calls, cfg)
	assert.Len(t, result.Conflicts, 1)
}

// A project that legitimately contains both languages is not a disagreement:
// reporting it would train a reviewer to ignore the list.
func TestBothLanguagesRepresentedIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}
	var calls []EvidenceToolCall
	for i := 0; i < 6; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"main.go"}, false, ""))
	}
	for i := 0; i < 4; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"tool.py"}, false, ""))
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts,
		"the claimed language is at least as represented as the alternative")
}

// Agreement is not news: a claim the evidence supports produces nothing.
func TestAgreementProducesNoConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}
	var calls []EvidenceToolCall
	for i := 0; i < 10; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"main.go"}, false, ""))
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts)
}

// Extensions that name a format rather than a language contribute nothing:
// "the project is Markdown" would be nonsense.
func TestFormatExtensionsAreNotLanguageEvidence(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}
	var calls []EvidenceToolCall
	for i := 0; i < 20; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"README.md", "data.json"}, false, ""))
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "a document format is not a programming language")
}

// --- path conflicts ---

// A path the memory names that was tried and never worked is a strong signal:
// something used the path, and the world said no.
//
// The evidence line names the NEWEST run that witnessed the failure, so the two
// calls carry distinct timestamps. Leaving them identical made the test depend
// on map iteration order, which passed locally and failed on CI — the label is
// now decided by the data.
func TestPathConflictReported(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "path", Value: "configs/app.yaml"}},
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()

	first := call("run-1", "file.read", []string{"configs/app.yaml"}, true, "no such file or directory")
	first.At = older
	second := call("run-2", "file.read", []string{"configs/app.yaml"}, true, "no such file or directory")
	second.At = newer

	result := VerifyClaims(claims, []EvidenceToolCall{first, second}, DefaultVerificationConfig())

	require.Len(t, result.Conflicts, 1)
	c := result.Conflicts[0]
	assert.Equal(t, "path", c.Kind)
	assert.Contains(t, c.Observed, "always failed")
	assert.Contains(t, c.Observed, "no such file", "the failure reason is shown")
	assert.Contains(t, c.Evidence, "run-2", "the newest witness is named")
	assert.Equal(t, 2, c.Count)
}

// A path that worked is not a disagreement, even if later reads failed: the
// path exists, and a later failure is a different problem from the memory being
// wrong.
func TestPathThatEverWorkedIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "path", Value: "configs/app.yaml"}},
	}
	calls := []EvidenceToolCall{
		call("run-1", "file.read", []string{"configs/app.yaml"}, false, ""),
		call("run-2", "file.read", []string{"configs/app.yaml"}, true, "permission denied"),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "one success proves the path exists")
}

// A path nobody ever touched produces no conflict. Most paths in a project are
// never visited by any recorded call, so "never seen" would report almost every
// path claim — this is why the rule is "attempted and failed" rather than
// "absent".
func TestUntouchedPathIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "path", Value: "configs/never-touched.yaml"}},
	}
	calls := []EvidenceToolCall{
		call("run-1", "file.read", []string{"configs/other.yaml"}, false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "no attempt is not evidence of absence")
}

// Path matching is case-insensitive, so a memory's capitalisation does not
// hide a real disagreement.
func TestPathMatchingIsCaseInsensitive(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "path", Value: "Configs/App.YAML"}},
	}
	calls := []EvidenceToolCall{
		call("run-1", "file.read", []string{"configs/app.yaml"}, true, "missing"),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Len(t, result.Conflicts, 1)
}

// --- the rule that shapes everything ---

// A conflict names both sides and does NOT declare one wrong. The evidence is a
// sample of what the runs touched, not a census, so which side is wrong is not
// knowable here — the same rule recall applies to conflicting memories.
func TestConflictReportsBothSidesWithoutVerdict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}
	var calls []EvidenceToolCall
	for i := 0; i < 8; i++ {
		calls = append(calls, call("run-1", "file.read", []string{"main.py"}, false, ""))
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	require.Len(t, result.Conflicts, 1)

	c := result.Conflicts[0]
	// Both sides are present as data.
	assert.Equal(t, "go", c.Claimed)
	assert.Equal(t, "python", c.Observed)
	// And there is no field asserting which is right. The type is the contract:
	// a Conflict carries a claim, an observation and evidence — nothing more.
	assert.Less(t, c.Confidence, 1.0, "confidence stays below certainty: it is a sample")
}

// --- unhandled kinds ---

// Services and versions belong to the next pass: an unhandled claim kind is
// unanswered, not a disagreement.
func TestUnhandledKindsProduceNoConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {
			{Kind: "service", Value: "api.example.com"},
			{Kind: "version", Value: "v2.1"},
		},
	}
	calls := []EvidenceToolCall{call("run-1", "shell.run", []string{"main.py"}, false, "")}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts)
	assert.Equal(t, 2, result.FactsChecked, "but they were counted as checked")
}

// --- evidence window ---

// Only the newest calls are examined: verification asks about the current
// world, so old evidence is worse than no evidence.
func TestEvidenceWindowKeepsNewest(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}

	// Eight ancient python calls and six recent Go calls.
	var calls []EvidenceToolCall
	old := time.Now().Add(-100 * 24 * time.Hour)
	for i := 0; i < 8; i++ {
		c := call("old-run", "file.read", []string{"main.py"}, false, "")
		c.At = old
		calls = append(calls, c)
	}
	for i := 0; i < 6; i++ {
		calls = append(calls, call("new-run", "file.read", []string{"main.go"}, false, ""))
	}

	cfg := DefaultVerificationConfig()
	cfg.MaxCalls = 6 // only the newest six
	result := VerifyClaims(claims, calls, cfg)

	assert.Equal(t, 6, result.CallsExamined)
	assert.Empty(t, result.Conflicts, "the recent evidence agrees with the claim")
}

// --- no evidence ---

// With no calls there is nothing to check, and the result says so rather than
// implying agreement.
func TestNoEvidenceProducesNoConflicts(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "language", Value: "go"}},
	}
	result := VerifyClaims(claims, nil, DefaultVerificationConfig())

	assert.Empty(t, result.Conflicts)
	assert.Zero(t, result.CallsExamined, "the count is how a caller tells 'no conflicts' from 'no evidence'")
	assert.Equal(t, 1, result.FactsChecked)
}

// --- determinism and ordering ---

// Conflicts come out strongest first, then by entry and kind, so two runs of
// the same verification produce the same list.
func TestConflictsAreSorted(t *testing.T) {
	conflicts := []Conflict{
		{EntryID: "b", Kind: "path", Confidence: 0.5},
		{EntryID: "a", Kind: "language", Confidence: 0.9},
		{EntryID: "c", Kind: "language", Confidence: 0.9},
	}
	sorted := SortedConflicts(conflicts)
	require.Len(t, sorted, 3)
	assert.Equal(t, "a", sorted[0].EntryID)
	assert.Equal(t, "c", sorted[1].EntryID)
	assert.Equal(t, "b", sorted[2].EntryID)

	// The input is not reordered underneath the caller.
	input := []Conflict{{EntryID: "z", Confidence: 0.1}, {EntryID: "a", Confidence: 0.9}}
	_ = SortedConflicts(input)
	assert.Equal(t, "z", input[0].EntryID)
}

// The same claims and evidence always produce the same result.
func TestVerificationIsDeterministic(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-a": {{Kind: "language", Value: "go"}},
		"mem-b": {{Kind: "path", Value: "a/b.yaml"}},
	}
	calls := []EvidenceToolCall{
		call("r1", "file.read", []string{"x.py"}, false, ""),
		call("r2", "file.read", []string{"a/b.yaml"}, true, "missing"),
	}
	for i := 0; i < 6; i++ {
		calls = append(calls, call("r1", "file.read", []string{"y.py"}, false, ""))
	}

	first := VerifyClaims(claims, calls, DefaultVerificationConfig())
	for i := 0; i < 5; i++ {
		next := VerifyClaims(claims, calls, DefaultVerificationConfig())
		require.Len(t, next.Conflicts, len(first.Conflicts))
		for j := range first.Conflicts {
			assert.Equal(t, first.Conflicts[j], next.Conflicts[j], "run %d differs at %d", i, j)
		}
	}
}

// --- config ---

func TestVerificationConfigDefaults(t *testing.T) {
	cfg := VerificationConfig{}.normalize()
	d := DefaultVerificationConfig()
	assert.Equal(t, d.MinEvidence, cfg.MinEvidence)
	assert.Equal(t, d.MaxCalls, cfg.MaxCalls)
	assert.False(t, cfg.Now.IsZero())

	// A negative threshold must not report a single observation as a conflict.
	cfg = VerificationConfig{MinEvidence: -1}.normalize()
	assert.Equal(t, d.MinEvidence, cfg.MinEvidence)
}

// --- extension mapping ---

// Unambiguous languages are mapped; formats are not, because "the project is
// JSON" would be nonsense.
func TestLanguageForExtension(t *testing.T) {
	for ext, want := range map[string]string{
		"go": "go", "py": "python", "rs": "rust", "ts": "typescript", "tsx": "typescript",
		"js": "javascript", "rb": "ruby", "sql": "sql",
	} {
		got, ok := languageForExtension(ext)
		require.True(t, ok, ext)
		assert.Equal(t, want, got, ext)
	}

	for _, ext := range []string{"md", "json", "yaml", "txt", "png"} {
		_, ok := languageForExtension(ext)
		assert.False(t, ok, "%s names a format, not a language", ext)
	}
}

// The shared path rule is one implementation, so the projector and the
// extractor cannot disagree about what a path is.
//
// Note the two extension lists answer DIFFERENT questions and are meant to
// differ: "is this a file we can name?" includes documents (md, json, yaml),
// while "which language is this?" must not — reporting "the project is
// Markdown" would be nonsense. Sharing one list would make one of the two
// wrong.
func TestPathExtensionKnownIsShared(t *testing.T) {
	for _, ext := range []string{"go", "py", "rs"} {
		_, ok := PathExtensionKnown(ext)
		assert.True(t, ok, "%s is a source file", ext)
	}
	for _, ext := range []string{"md", "json", "yaml"} {
		_, ok := PathExtensionKnown(ext)
		assert.True(t, ok, "%s is a file we can name", ext)

		_, isLang := languageForExtension(ext)
		assert.False(t, isLang, "%s names a format, not a language", ext)
	}
	// A leading dot is tolerated, so a caller holding ".py" need not trim it.
	_, ok := PathExtensionKnown(".py")
	assert.True(t, ok)

	// Something that is neither a file nor a language.
	_, ok = PathExtensionKnown("exe")
	assert.False(t, ok)
}
