package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// textCall builds an evidence call whose flattened text carries the payload.
func textCall(runID, text string, failed bool, errMsg string) EvidenceToolCall {
	return EvidenceToolCall{
		RunID: runID, ToolName: "shell.run", Text: text,
		Failed: failed, Error: errMsg, At: time.Now(),
	}
}

// --- version subject extraction ---

// A version is only checkable with its subject: "v2.1" could belong to any tool
// in the stack, so a bare number would be compared against unrelated things.
func TestVersionSubjectExtracted(t *testing.T) {
	found := ExtractAssertionsStatic("the project uses go 1.26 and node v20")

	bySubject := map[string]string{}
	for _, a := range found {
		if a.Kind == "version" {
			bySubject[a.Subject] = a.Value
		}
	}
	assert.Equal(t, "1.26", bySubject["go"])
	assert.Equal(t, "v20", bySubject["node"])
}

// "go version 1.26" works too: the connective between the name and the number
// is skipped rather than ending the search.
func TestVersionSubjectSkipsConnectives(t *testing.T) {
	found := ExtractAssertionsStatic("running go version 1.26")
	var got string
	for _, a := range found {
		if a.Kind == "version" {
			got = a.Subject
		}
	}
	assert.Equal(t, "go", got)
}

// A version whose preceding word is not a software name has NO subject, and is
// therefore not checkable. Attaching a version to an article or a preposition
// would invent comparisons.
func TestVersionWithoutSoftwareNameHasNoSubject(t *testing.T) {
	found := ExtractAssertionsStatic("the retry limit is 3 and the ratio is 1.5")

	for _, a := range found {
		if a.Kind == "version" {
			assert.Empty(t, a.Subject,
				"a version next to a non-software word must not claim a subject")
		}
	}
}

// --- version conflicts ---

// The evidence showing a different version of the SAME software is a
// disagreement.
func TestVersionConflictReported(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "1.26", Subject: "go"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "go 1.24 build output", false, ""),
		textCall("run-2", "go 1.24 again", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())

	require.Len(t, result.Conflicts, 1)
	c := result.Conflicts[0]
	assert.Equal(t, "version", c.Kind)
	assert.Contains(t, c.Claimed, "go")
	assert.Contains(t, c.Observed, "1.24")
	assert.Contains(t, c.Evidence, "run-")
	assert.LessOrEqual(t, c.Confidence, 0.7, "text evidence is weaker than a file extension")
}

// The claimed version being present means agreement, whatever else also
// appears: a project can mention an old version in a changelog.
func TestVersionPresentIsAgreement(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "1.26", Subject: "go"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "go 1.26", false, ""),
		textCall("run-2", "upgraded from go 1.24 to go 1.26", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "the claimed version appears, so the memory holds")
}

// A DIFFERENT software's version is not a disagreement: "python 3.11" in the
// logs says nothing about a claim about go.
func TestDifferentSoftwareVersionIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "1.26", Subject: "go"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "python 3.11 is installed", false, ""),
		textCall("run-2", "python 3.12 is installed", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "a version claim is about ITS software")
}

// A subjectless version claim is skipped rather than guessed at: the extractor
// refused to invent a subject, and verification must not undo that.
func TestSubjectlessVersionIsSkipped(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "2.1"}}, // no subject
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "go 1.24", false, ""),
		textCall("run-2", "go 1.24", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "without a subject there is nothing to compare")
	assert.Equal(t, 1, result.FactsChecked, "but it was still counted as checked")
}

// The version threshold is separate from the language one and configurable.
func TestVersionThreshold(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "1.26", Subject: "go"}},
	}
	calls := []EvidenceToolCall{textCall("run-1", "go 1.24", false, "")}

	// One occurrence is below the default of two.
	assert.Empty(t, VerifyClaims(claims, calls, DefaultVerificationConfig()).Conflicts)

	cfg := DefaultVerificationConfig()
	cfg.MinVersionEvidence = 1
	assert.Len(t, VerifyClaims(claims, calls, cfg).Conflicts, 1)
}

// --- service conflicts ---

// A service the memory names that was tried and never answered is a
// disagreement: something reached for it and the world refused.
func TestServiceConflictReported(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "service", Value: "api.example.com"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "connecting to api.example.com", true, "connection refused"),
		textCall("run-2", "connecting to api.example.com", true, "connection refused"),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())

	require.Len(t, result.Conflicts, 1)
	c := result.Conflicts[0]
	assert.Equal(t, "service", c.Kind)
	assert.Contains(t, c.Observed, "always failed")
	assert.Contains(t, c.Evidence, "2 failed attempt(s)")
	assert.LessOrEqual(t, c.Confidence, 0.7)
}

// A service that answered at least once exists: a later failure is a different
// problem from the memory being wrong.
func TestServiceThatEverAnsweredIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "service", Value: "api.example.com"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "api.example.com responded", false, ""),
		textCall("run-2", "api.example.com timeout", true, "i/o timeout"),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "one success proves the service exists")
}

// A service nobody tried produces no conflict. A project connects to many
// things and its runs touch few, so "never seen" would flag almost every
// service claim.
func TestUntriedServiceIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "service", Value: "never-touched.example.com"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "other.example.com worked", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "no attempt is not evidence of absence")
}

// The evidence showing a DIFFERENT service is not a disagreement: a project
// using several services is ordinary.
func TestDifferentServiceIsNotAConflict(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "service", Value: "api.example.com"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "cache.example.com responded", false, ""),
		textCall("run-2", "cache.example.com responded", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts,
		"the memory says what IS used, not what is the only thing used")
}

// A single failed attempt is as likely to be a transient network problem as a
// wrong memory, so the service threshold is its own.
func TestServiceThreshold(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "service", Value: "api.example.com"}},
	}
	calls := []EvidenceToolCall{
		textCall("run-1", "api.example.com", true, "connection refused"),
	}

	assert.Empty(t, VerifyClaims(claims, calls, DefaultVerificationConfig()).Conflicts,
		"one failure is not enough by default")

	cfg := DefaultVerificationConfig()
	cfg.MinServiceAttempts = 1
	assert.Len(t, VerifyClaims(claims, calls, cfg).Conflicts, 1)
}

// --- word boundaries (the fourth substring lesson, applied) ---

// "go" must not match "going", and a hostname must not match a longer token:
// the extractor already learned this, and the evidence indexer uses the same
// rule so the two cannot disagree.
func TestEvidenceIndexingRespectsWordBoundaries(t *testing.T) {
	claims := map[string][]Assertion{
		"mem-1": {{Kind: "version", Value: "1.26", Subject: "go"}},
	}
	// "going 1.24" contains a version but not the software "go".
	calls := []EvidenceToolCall{
		textCall("run-1", "going 1.24", false, ""),
		textCall("run-2", "going 1.24", false, ""),
	}

	result := VerifyClaims(claims, calls, DefaultVerificationConfig())
	assert.Empty(t, result.Conflicts, "\"going\" is not the software \"go\"")
}

// --- pairing symmetry ---

// The extractor and the evidence indexer must attach subjects by the SAME rule,
// or every comparison would be between unrelated things.
func TestVersionPairingUsesTheExtractorRule(t *testing.T) {
	// Text the extractor would parse into a (go, 1.26) claim...
	found := ExtractAssertionsStatic("the project uses go 1.26")
	var extracted *Assertion
	for i := range found {
		if found[i].Kind == "version" {
			extracted = &found[i]
		}
	}
	require.NotNil(t, extracted)

	// ...must pair the same way when the same text arrives as evidence.
	pairs := versionPairs("the project uses go 1.26")
	require.Len(t, pairs, 1)
	assert.Equal(t, extracted.Subject, pairs[0].Subject)
	assert.Equal(t, extracted.Value, pairs[0].Version)
}

// --- config ---

func TestVerificationConfigHasAllThresholds(t *testing.T) {
	cfg := VerificationConfig{}.normalize()
	d := DefaultVerificationConfig()
	assert.Equal(t, d.MinEvidence, cfg.MinEvidence)
	assert.Equal(t, d.MinVersionEvidence, cfg.MinVersionEvidence)
	assert.Equal(t, d.MinServiceAttempts, cfg.MinServiceAttempts)
	assert.Equal(t, d.MaxCalls, cfg.MaxCalls)

	// A negative threshold falls back rather than reporting on one observation.
	cfg = VerificationConfig{MinVersionEvidence: -1, MinServiceAttempts: -1}.normalize()
	assert.Equal(t, d.MinVersionEvidence, cfg.MinVersionEvidence)
	assert.Equal(t, d.MinServiceAttempts, cfg.MinServiceAttempts)
}
