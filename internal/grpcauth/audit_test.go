package grpcauth

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/identity"
)

// captureAudit runs fn and returns everything written to the standard logger.
//
// The audit lines go through log.Printf, which is what this process already uses
// everywhere, so capturing the logger is capturing the audit trail — not a
// stand-in for it.
func captureAudit(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	fn()
	return buf.String()
}

// A successful token call is recorded, as whom, and by what mechanism.
func TestAuditRecordsASuccessfulTokenCall(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:alice-token")}

	out := captureAudit(t, func() {
		_, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"alice-token"))
		require.NoError(t, err)
	})

	assert.Contains(t, out, auditPrefix)
	assert.Contains(t, out, `principal="alice"`)
	assert.Contains(t, out, "verified=true")
	assert.Contains(t, out, "mechanism="+verifierName)
}

// A rejected call is recorded too.
//
// Failures are the half an investigation usually wants: success is what the
// workflow log already shows, while a burst of rejections is the only visible
// sign that someone is guessing.
func TestAuditRecordsARejection(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:alice-token")}

	out := captureAudit(t, func() {
		_, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"wrong"))
		require.Error(t, err)
	})

	assert.Contains(t, out, auditPrefix)
	assert.Contains(t, out, "rejected")
	assert.Contains(t, out, "invalid_or_missing_credential")
	// The deployment's posture is safe to disclose; it says nothing about the
	// credential that was guessed.
	assert.Contains(t, out, "tokens_configured=true")
}

// THE SECURITY PROPERTY: the audit log never contains the token.
//
// This is the assertion the whole design exists for. Audit lines are copied into
// tickets, aggregators and screenshots; a line carrying the credential would
// spread the very secret it was meant to help protect — and a rotation triggered
// because of a leak would then widen it.
func TestAuditNeverContainsTheCredential(t *testing.T) {
	const token = "super-secret-token-value"
	cfg := Config{Tokens: ParseTokens("alice:" + token)}

	out := captureAudit(t, func() {
		_, _ = cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+token))
		_, _ = cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"wrong-value"))
	})

	require.NotEmpty(t, out, "there must be lines to inspect")
	assert.NotContains(t, out, token, "the token itself must never be written")
	assert.NotContains(t, out, "wrong-value", "nor a guess that failed")
}

// The shared secret's value is likewise never written.
func TestAuditNeverContainsTheSharedSecret(t *testing.T) {
	const secret = "the-shared-secret"
	cfg := Config{Secret: secret}

	out := captureAudit(t, func() {
		_, _ = cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+secret))
		_, _ = cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"not-it"))
	})

	require.NotEmpty(t, out)
	assert.NotContains(t, out, secret)
	assert.NotContains(t, out, "not-it")
}

// A successful shared-secret call is recorded with NO principal, and says which
// mechanism admitted it.
//
// Without the mechanism the line would be indistinguishable from a token call
// with no identity, and "how did this caller get in" is exactly what an
// investigation asks.
func TestAuditRecordsASharedSecretCallWithoutAPrincipal(t *testing.T) {
	cfg := Config{Secret: "s3cret"}

	out := captureAudit(t, func() {
		_, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"s3cret"))
		require.NoError(t, err)
	})

	assert.Contains(t, out, `principal="unattributed"`,
		"a shared secret establishes no identity, and the line says so")
	assert.Contains(t, out, "mechanism=shared-secret")
	assert.Contains(t, out, "verified=false")
}

// The fingerprint identifies a token WITHOUT being usable as one.
//
// It answers the question an investigation asks — "is this the same credential
// as the calls in that other window" — while a full log dump still yields no
// credential.
func TestFingerprintIsStableAndShort(t *testing.T) {
	first := tokenFingerprint("some-token")
	second := tokenFingerprint("some-token")
	other := tokenFingerprint("some-other-token")

	assert.Equal(t, first, second, "the same token fingerprints identically")
	assert.NotEqual(t, first, other, "different tokens do not collide here")
	assert.Len(t, first, fingerprintLen)
	assert.NotContains(t, first, "some-token", "the fingerprint is not the token")
}

// An absent credential fingerprints as "-" rather than as the digest of the
// empty string: "presented nothing" and "presented something that hashes to
// this" are different facts, and collapsing them hides the commonest failure.
func TestEmptyCredentialFingerprintsAsADash(t *testing.T) {
	assert.Equal(t, "-", tokenFingerprint(""))
	assert.Equal(t, "-", tokenFingerprint("   "))
	assert.NotEqual(t, "-", tokenFingerprint("x"))
}

// Every rejection path produces exactly one audit line.
//
// A path that returned without auditing would be a silent rejection — the defect
// this change exists to end — and it would be invisible in tests that only check
// the error.
func TestEveryRejectionPathIsAudited(t *testing.T) {
	cases := map[string]struct {
		cfg    Config
		header string
	}{
		"token configured, none presented":  {Config{Tokens: ParseTokens("alice:t")}, ""},
		"token configured, wrong presented": {Config{Tokens: ParseTokens("alice:t")}, bearerPrefix + "nope"},
		"secret configured, too short":      {Config{Secret: "s3cret"}, bearerPrefix + "x"},
		"secret configured, wrong length":   {Config{Secret: "s3cret"}, bearerPrefix + "wronglength"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := captureAudit(t, func() {
				_, err := tc.cfg.authorize(metadataWith(authorizationHeader, tc.header))
				require.Error(t, err)
			})
			assert.Equal(t, 1, strings.Count(out, auditPrefix),
				"exactly one audit line per rejected call")
			assert.Contains(t, out, "rejected")
		})
	}
}

// A deployment with no credential at all audits nothing on loopback.
//
// There was no authentication, so there is nothing to record — and a line per
// call would bury the real events under routine local traffic. This is a
// deliberate silence, not an oversight, which is why it is asserted.
func TestNoCredentialConfiguredIsNotAudited(t *testing.T) {
	var cfg Config // no secret, no tokens

	out := captureAudit(t, func() {
		authed, err := cfg.authorize(metadataWith(authorizationHeader, ""))
		require.NoError(t, err, "a loopback deployment with no credential admits the call")
		require.NotNil(t, authed)
	})

	assert.Empty(t, out, "nothing was authenticated, so nothing is audited")
}

// The line names the verifying mechanism, so it cannot be confused with the
// shared secret's anonymous admission.
func TestAuditDistinguishesMechanisms(t *testing.T) {
	tokenCfg := Config{Tokens: ParseTokens("alice:alice-token")}
	secretCfg := Config{Secret: "s3cret"}

	var tokenOut, secretOut string
	tokenOut = captureAudit(t, func() {
		_, _ = tokenCfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"alice-token"))
	})
	secretOut = captureAudit(t, func() {
		_, _ = secretCfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"s3cret"))
	})

	assert.Contains(t, tokenOut, "mechanism="+verifierName)
	assert.Contains(t, secretOut, "mechanism=shared-secret")
	assert.NotEqual(t, tokenOut, secretOut)
}

// The audited principal matches what lands on the context, so the log and the
// request cannot disagree about who called.
func TestAuditedPrincipalMatchesTheContext(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:alice-token")}

	var fromContext identity.Actor
	out := captureAudit(t, func() {
		authed, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"alice-token"))
		require.NoError(t, err)
		fromContext = identity.FromContext(authed)
	})

	assert.Equal(t, "alice", fromContext.ID())
	assert.Contains(t, out, "principal="+`"`+fromContext.ID()+`"`,
		"the line must name the principal the handler will see")
}
