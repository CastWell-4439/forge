package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- layers ---

// The zero value is episodic, and that is the safety property: an unlabelled
// memory is an observation about one run, never a claim about the world.
func TestLayerDefaultsToEpisodic(t *testing.T) {
	assert.Equal(t, LayerEpisodic, NormalizeMemoryLayer(""))
	assert.Equal(t, LayerEpisodic, NormalizeMemoryLayer("unknown"))
	// A typo must not promote an observation into a claim.
	assert.Equal(t, LayerEpisodic, NormalizeMemoryLayer("factual"))
	assert.Equal(t, LayerEpisodic, NormalizeMemoryLayer("FACT"))

	assert.Equal(t, LayerFact, NormalizeMemoryLayer("fact"))
}

// --- static assertion extraction ---

// Languages are matched as whole words: "go" must not match "going" or
// "django". Substring matching here would invent assertions, and an invented
// assertion makes the whole verification path untrustworthy.
func TestExtractLanguageAssertions(t *testing.T) {
	found := ExtractAssertionsStatic("the project is maintained in Go and deployed with terraform")
	kinds := map[string]string{}
	for _, a := range found {
		kinds[a.Kind+":"+a.Value] = a.Source
	}
	assert.Contains(t, kinds, "language:go")
	assert.Contains(t, kinds, "language:terraform")
	assert.Equal(t, ExtractionRule, kinds["language:go"], "the static pass marks its own findings")
}

func TestExtractDoesNotMatchSubstrings(t *testing.T) {
	// "going" contains "go"; "django" contains "go". Neither is the language.
	found := ExtractAssertionsStatic("we are going to fix the django view")
	for _, a := range found {
		assert.NotEqual(t, "go", a.Value, "a substring must not become a language assertion")
	}
}

// Paths are recognised by their extension, so a dotted token that is not a file
// is not a path.
func TestExtractPathAssertions(t *testing.T) {
	found := ExtractAssertionsStatic("the handler lives in internal/agent/core/tools.go")

	// Locate by kind rather than by position: the same text also contains the
	// word "go", so the list holds both assertions and their order is the
	// deterministic sort, not the order of discovery.
	var path *Assertion
	for i := range found {
		if found[i].Kind == "path" {
			path = &found[i]
			break
		}
	}
	require.NotNil(t, path, "the .go path is extracted")
	assert.Equal(t, "go", path.Value, "the extension is the value")
	assert.Equal(t, "internal/agent/core/tools.go", path.Span, "the span is the original text")
}

// A dotted token that is not a source file is not a path: the rule keys on a
// known extension, because "1.5" and "api.example.com" also contain dots.
func TestNonPathDottedTokensAreNotPaths(t *testing.T) {
	for _, a := range ExtractAssertionsStatic("version 1.5 runs at api.example.com") {
		assert.NotEqual(t, "path", a.Kind,
			"a version or a hostname must not be read as a file path")
	}
}

// Versions need a v prefix or a dotted number: a bare count is not a version,
// and a date is not one either.
func TestExtractVersionAssertions(t *testing.T) {
	values := map[string]bool{}
	for _, a := range ExtractAssertionsStatic("upgrade to v2.1 and go 1.26, then check 100rps") {
		if a.Kind == "version" {
			values[a.Value] = true
		}
	}
	assert.True(t, values["v2.1"], "a v-prefixed version")
	assert.True(t, values["1.26"], "a dotted version")

	// A bare count and a date are not versions.
	assert.False(t, values["100rps"])
	assert.False(t, values["2026-01-01"])
}

// Services are host:port or dotted hostnames; a bare word says nothing a
// checker can use.
func TestExtractServiceAssertions(t *testing.T) {
	values := map[string]bool{}
	for _, a := range ExtractAssertionsStatic("the api runs at api.example.com and the db at db:5432") {
		if a.Kind == "service" {
			values[a.Value] = true
		}
	}
	assert.True(t, values["api.example.com"])
	assert.True(t, values["db:5432"])
}

// A memory that states nothing checkable produces no assertions — which is
// itself a finding, because it is what routes an entry to discard rather than
// verify.
func TestExtractFindsNothingInVagueText(t *testing.T) {
	found := ExtractAssertionsStatic("the run went fine and everyone was happy")
	assert.Empty(t, found)
}

// Duplicate values are reported once, whichever pass found them.
func TestMergeAssertionsPrefersRuleOnTies(t *testing.T) {
	rule := []Assertion{{Kind: "language", Value: "go", Source: ExtractionRule}}
	model := []Assertion{
		{Kind: "language", Value: "go", Source: ExtractionModel},
		{Kind: "service", Value: "api.example.com", Source: ExtractionModel},
	}

	merged := MergeAssertions(rule, model)
	require.Len(t, merged, 2, "the duplicate is dropped")

	byKey := map[string]Assertion{}
	for _, a := range merged {
		byKey[a.Kind+":"+a.Value] = a
	}
	assert.Equal(t, ExtractionRule, byKey["language:go"].Source,
		"on a tie the reproducible finding is the one attributed")
	assert.Equal(t, ExtractionModel, byKey["service:api.example.com"].Source,
		"a model-only finding keeps its origin")
}

// Merging is deterministic, so two reviews of one store are comparable.
func TestMergeAssertionsIsOrdered(t *testing.T) {
	merged := MergeAssertions(nil, []Assertion{
		{Kind: "service", Value: "z.example.com"},
		{Kind: "language", Value: "go"},
		{Kind: "language", Value: "rust"},
	})
	require.Len(t, merged, 3)
	assert.Equal(t, "language", merged[0].Kind)
	assert.Equal(t, "go", merged[0].Value)
	assert.Equal(t, "rust", merged[1].Value)
	assert.Equal(t, "service", merged[2].Kind)
}

// --- candidate ordering ---

// Strongest proposals first, ties broken by id so repeated reviews agree.
func TestSortedCandidates(t *testing.T) {
	sorted := SortedCandidates([]MemoryCandidate{
		{EntryID: "b", Confidence: 0.3},
		{EntryID: "a", Confidence: 0.8},
		{EntryID: "c", Confidence: 0.8},
	})
	require.Len(t, sorted, 3)
	assert.Equal(t, "a", sorted[0].EntryID)
	assert.Equal(t, "c", sorted[1].EntryID)
	assert.Equal(t, "b", sorted[2].EntryID)

	// The input is not reordered underneath the caller.
	input := []MemoryCandidate{{EntryID: "z", Confidence: 0.1}, {EntryID: "a", Confidence: 0.9}}
	_ = SortedCandidates(input)
	assert.Equal(t, "z", input[0].EntryID)
}
