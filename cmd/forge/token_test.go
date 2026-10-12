package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runTokenCmd invokes the token command and captures its output.
func runTokenCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runToken(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// field extracts "name: value" from the command's output.
func field(t *testing.T, output, name string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if after, found := strings.CutPrefix(line, name+":"); found {
			return strings.TrimSpace(after)
		}
	}
	t.Fatalf("no %q line in output:\n%s", name, output)
	return ""
}

// The digest the command prints is one the SERVER accepts.
//
// This is the property that matters: a hash utility whose output the server
// rejects is worse than no utility, because it looks like it worked.
func TestTokenHashProducesAnAcceptedEntry(t *testing.T) {
	code, stdout, stderr := runTokenCmd(t, "hash", "my-secret")
	require.Equal(t, 0, code, "stderr: %s", stderr)

	digest := field(t, stdout, "digest")
	assert.True(t, strings.HasPrefix(digest, "sha256:"))

	// Feed the digest back through the real parser, as a deployment would.
	code, verifyOut, verifyErr := runTokenCmd(t, "verify", "my-secret", "--tokens", "alice:"+digest)
	require.Equal(t, 0, code, "the server must accept what this command printed; stderr: %s", verifyErr)
	assert.Contains(t, verifyOut, "alice")
	assert.Contains(t, verifyOut, "verified:  true")
}

// A generated token is printed in full, because only its digest is stored and
// there is no way to recover it afterwards.
func TestGeneratePrintsTheTokenItself(t *testing.T) {
	code, stdout, stderr := runTokenCmd(t, "hash", "--generate")
	require.Equal(t, 0, code, "stderr: %s", stderr)

	token := field(t, stdout, "token")
	assert.NotEmpty(t, token, "a generated token must be shown, or it is lost")
	digest := field(t, stdout, "digest")

	// And it verifies, which is what makes the printed token the real one.
	code, _, _ = runTokenCmd(t, "verify", token, "--tokens", "alice:"+digest)
	assert.Equal(t, 0, code, "the printed token must be the one the digest matches")
}

// Two generated tokens differ. A generator that repeated itself would hand every
// principal the same credential.
func TestGeneratedTokensDiffer(t *testing.T) {
	_, first, _ := runTokenCmd(t, "hash", "--generate")
	_, second, _ := runTokenCmd(t, "hash", "--generate")

	assert.NotEqual(t, field(t, first, "token"), field(t, second, "token"))
}

// An expiry is validated by the command that writes the line, so a typo is
// reported here rather than by a service that will not start.
func TestHashRejectsABadExpiry(t *testing.T) {
	code, _, stderr := runTokenCmd(t, "hash", "tok", "--expires", "tomorrow")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "RFC 3339")
}

// A well-formed expiry is echoed into the entry, ready to paste.
func TestHashAcceptsAGoodExpiry(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	code, stdout, stderr := runTokenCmd(t, "hash", "tok", "--expires", expiry)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	assert.Contains(t, field(t, stdout, "entry"), "expires="+expiry)
}

// Verify reports no match for an unknown token, and an expired one identically —
// it stands in for the server and must not reveal more than the server does.
func TestVerifyReportsUnknownAndExpiredTheSameWay(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	codeUnknown, outUnknown, _ := runTokenCmd(t, "verify", "nope", "--tokens", "alice:tok")
	codeExpired, outExpired, _ := runTokenCmd(t, "verify", "tok", "--tokens", "alice:tok:expires="+past)

	assert.Equal(t, 1, codeUnknown)
	assert.Equal(t, 1, codeExpired)
	assert.Equal(t, outUnknown, outExpired, "the two outcomes must be told apart by nothing")
	assert.Contains(t, outUnknown, "unknown or expired")
}

// A malformed list is reported as malformed, not as "no match": the two have
// different fixes, and saying "no match" sends an operator to the wrong place.
func TestVerifyReportsAMalformedListDistinctly(t *testing.T) {
	code, _, stderr := runTokenCmd(t, "verify", "tok", "--tokens", "alice")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "malformed")
	assert.NotContains(t, stderr, "no match")
}

// Missing arguments are refused with a message rather than a panic.
func TestTokenRequiresItsArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"hash"},
		{"verify"},
		{"verify", "tok"},
		{"verify", "--tokens", "alice:x"},
		{"nonsense"},
	} {
		code, _, stderr := runTokenCmd(t, args...)
		assert.Equal(t, 1, code, "args %v must fail", args)
		assert.NotEmpty(t, stderr)
	}
}

// --generate with an explicit token is contradictory, and saying so beats
// silently picking one.
func TestGenerateAndExplicitTokenConflict(t *testing.T) {
	code, _, stderr := runTokenCmd(t, "hash", "--generate", "explicit")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "mutually exclusive")
}

// An entry template names the principal as a placeholder: this command does not
// know who the token is for, and inventing a name would produce a line that looks
// ready to use while attributing the credential to the wrong person.
func TestEntryLeavesThePrincipalAsAPlaceholder(t *testing.T) {
	_, stdout, _ := runTokenCmd(t, "hash", "tok")

	assert.Contains(t, field(t, stdout, "entry"), "<principal>")
}
