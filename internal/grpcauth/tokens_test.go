package grpcauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/castwell/forge/internal/identity"
)

// digestOf is the "sha256:" form a deployment would put in the environment.
func digestOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return plaintextDigestPrefix + hex.EncodeToString(sum[:])
}

// --- parsing ---

// A plaintext token authenticates its principal, and the result is VERIFIED.
//
// This is the promise the identity package made and could not keep: until now
// nothing could produce a verified Actor, so Verified() was always false.
func TestPlaintextTokenResolvesToAVerifiedPrincipal(t *testing.T) {
	set := ParseTokens("alice:token-a")

	actor, ok := set.Resolve("token-a")
	require.True(t, ok)
	assert.Equal(t, "alice", actor.ID())
	assert.True(t, actor.Verified(), "a token check is a verification")
	assert.Equal(t, verifierName, actor.Verifier(),
		"an audit entry must name what verified it, not merely claim it was verified")
}

// The hashed form authenticates identically. It is the recommended form: the
// server then holds only a digest, so reading the configuration does not yield a
// usable credential.
func TestHashedTokenResolvesIdentically(t *testing.T) {
	set := ParseTokens("alice:" + digestOf("token-a"))

	actor, ok := set.Resolve("token-a")
	require.True(t, ok)
	assert.Equal(t, "alice", actor.ID())
	assert.True(t, actor.Verified())

	// The digest itself is NOT a working credential, which is the property that
	// makes storing it safe.
	_, ok = set.Resolve(digestOf("token-a"))
	assert.False(t, ok, "being able to read the digest must not be enough to authenticate")
}

// Plaintext and hashed entries work in the same list, so a deployment can migrate
// one principal at a time rather than all at once.
func TestMixedFormsCoexist(t *testing.T) {
	set := ParseTokens("alice:token-a,bob:" + digestOf("token-b"))

	for name, token := range map[string]string{"alice": "token-a", "bob": "token-b"} {
		actor, ok := set.Resolve(token)
		require.True(t, ok, "%s must authenticate", name)
		assert.Equal(t, name, actor.ID())
	}
}

// A wrong token resolves to nobody.
func TestUnknownTokenResolvesToNobody(t *testing.T) {
	set := ParseTokens("alice:token-a")

	_, ok := set.Resolve("not-the-token")
	assert.False(t, ok)
}

// A malformed entry is RECORDED, not skipped: skipping would leave a deployment
// looking healthy while a principal silently cannot authenticate.
func TestMalformedEntriesAreRecorded(t *testing.T) {
	cases := map[string]string{
		"no separator":     "alice",
		"no token":         "alice:",
		"no principal":     ":token-a",
		"bad hex":          "alice:sha256:not-hex",
		"wrong hex length": "alice:sha256:abcd",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			set := ParseTokens(raw)
			assert.Error(t, set.Error(), "a malformed entry must be reported")
		})
	}
}

// The malformed-entry message does not echo the entry: a malformed line is
// exactly where a secret is most likely to be sitting in plain sight.
func TestMalformedEntryDoesNotEchoTheSecret(t *testing.T) {
	set := ParseTokens("alice:sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00")

	require.Error(t, set.Error())
	assert.NotContains(t, set.Error().Error(), "0123456789abcdef",
		"an error message must not quote the credential it rejected")
}

// An empty value configures nothing; a present-but-empty list is a MISTAKE.
//
// The distinction matters. An unset variable is a posture (no tokens), while a
// variable that is set but names nobody would leave Enabled() reporting a
// credential that no principal can satisfy — a routable deployment would start
// and then refuse every caller. It is reported instead.
func TestEmptyTokenListVersusPresentButEmpty(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		set := ParseTokens(raw)
		assert.False(t, set.Configured(), "input %q is not configured", raw)
		assert.NoError(t, set.Error())
	}

	for _, raw := range []string{",", ",,,", "  ,  "} {
		set := ParseTokens(raw)
		require.Error(t, set.Error(), "input %q names nobody and must be reported", raw)
		assert.Contains(t, set.Error().Error(), "names no principal")
	}
}

// A trailing comma is tolerated: it is punctuation, not a missing entry, and
// rejecting it would make the variable awkward to build in a loop.
func TestTrailingCommaIsTolerated(t *testing.T) {
	set := ParseTokens("alice:token-a,")

	require.NoError(t, set.Error())
	assert.Equal(t, 1, set.Len())
}

// A present-but-empty list refuses the start, so the operator learns at startup
// rather than from callers being turned away.
func TestPresentButEmptyListRefusesToStart(t *testing.T) {
	cfg := Config{Addr: "0.0.0.0:50051", Tokens: ParseTokens(",")}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names no principal")
}

// Names are exposed, tokens are not — a log line or error message that listed
// credentials would defeat the point of storing digests.
func TestNamesAreExposedAndSortedButTokensAreNot(t *testing.T) {
	set := ParseTokens("zoe:z-token,alice:a-token,mike:m-token")

	assert.Equal(t, []string{"alice", "mike", "zoe"}, set.Names())
}

// --- the security invariant ---

// A VERIFIED principal beats a DECLARED one.
//
// This is the invariant that makes verification worth anything. A caller holding
// a valid token of their own can also state a name in the header — and if the
// declaration won, holding any valid token would be enough to be anyone.
func TestVerifiedPrincipalBeatsADeclaredOne(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:token-a")}

	// The caller presents alice's token but claims to be bob in the header.
	ctx := metadataWith(authorizationHeader, bearerPrefix+"token-a")
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		authorizationHeader, bearerPrefix+"token-a",
		identity.GRPCMetadataKey, "bob",
	))

	authed, err := cfg.authorize(ctx)
	require.NoError(t, err)

	afterIdentity := withDeclaredActorForTest(authed)
	actor := identity.FromContext(afterIdentity)

	assert.Equal(t, "alice", actor.ID(),
		"the token says who this is; the header is a claim and must not override it")
	assert.True(t, actor.Verified())
}

// With no token, a declaration is still only a declaration.
func TestDeclarationAloneStaysUnverified(t *testing.T) {
	cfg := Config{Secret: "s3cret"}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		authorizationHeader, bearerPrefix+"s3cret",
		identity.GRPCMetadataKey, "bob",
	))

	authed, err := cfg.authorize(ctx)
	require.NoError(t, err)

	afterIdentity := withDeclaredActorForTest(authed)
	actor := identity.FromContext(afterIdentity)

	assert.Equal(t, "bob", actor.ID())
	assert.False(t, actor.Verified(),
		"the shared secret admits the caller but establishes no identity")
}

// withDeclaredActorForTest runs the REAL identity interceptor over a context.
//
// It calls the production interceptor rather than reimplementing what it does,
// so this test exercises the actual chain: credential interceptor first,
// identity interceptor second, and the rule that the second must not overwrite
// the first.
func withDeclaredActorForTest(ctx context.Context) context.Context {
	var out context.Context
	_, _ = identity.UnaryServerInterceptor()(
		ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"},
		func(inner context.Context, _ any) (any, error) {
			out = inner
			return nil, nil
		},
	)
	return out
}

// --- coexistence with the shared secret ---

// The shared secret still admits a caller — and produces NO verified identity.
//
// This is the difference between the two credentials rather than a temporary
// state: a shared secret cannot distinguish its holders, so there is no
// principal to record.
func TestSharedSecretStillAdmitsWithoutIdentity(t *testing.T) {
	cfg := Config{Secret: "s3cret"}

	authed, err := cfg.authorize(metadataWith(authorizationHeader, "Bearer s3cret"))
	require.NoError(t, err)

	_, declared := identity.FromContext(authed).Declared()
	assert.False(t, declared)
	assert.False(t, identity.FromContext(authed).Verified())
}

// A valid token is accepted WITHOUT the shared secret: one call, one credential.
// Requiring both would be friction rather than security.
func TestTokenIsAcceptedWithoutTheSharedSecret(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:token-a")}

	_, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"token-a"))
	assert.NoError(t, err)
}

// A wrong token falls through to the shared secret rather than being rejected
// outright, so adding tokens does not lock out clients still using the old
// credential.
func TestWrongTokenStillAllowsTheSharedSecret(t *testing.T) {
	cfg := Config{Secret: "s3cret", Tokens: ParseTokens("alice:token-a")}

	_, err := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"s3cret"))
	assert.NoError(t, err, "a deployment adding tokens must not lock out its existing clients")
}

// Neither credential is a rejection, never a pass.
func TestNoCredentialIsRejectedWhenTokensAreConfigured(t *testing.T) {
	cfg := Config{Tokens: ParseTokens("alice:token-a")}

	_, err := cfg.authorize(metadataWith(authorizationHeader, ""))
	assert.Error(t, err, "a deployment that configured only tokens must still require one")
	assert.Equal(t, UnauthenticatedError, err)
}

// An unknown credential and a missing one produce the SAME error, so the reply
// carries no information about which part failed.
func TestMissingAndWrongCredentialsAreIndistinguishable(t *testing.T) {
	cfg := Config{Secret: "s3cret", Tokens: ParseTokens("alice:token-a")}

	_, missing := cfg.authorize(metadataWith(authorizationHeader, ""))
	_, wrong := cfg.authorize(metadataWith(authorizationHeader, bearerPrefix+"nope"))

	assert.Equal(t, UnauthenticatedError, missing)
	assert.Equal(t, UnauthenticatedError, wrong)
	assert.Equal(t, missing.Error(), wrong.Error())
}

// --- Env / Validate ---

// Either credential makes the deployment "authenticated": a token-only config
// must still reject unauthenticated calls.
func TestEnabledWithEitherCredential(t *testing.T) {
	assert.False(t, Config{}.Enabled())
	assert.True(t, Config{Secret: "s"}.Enabled())
	assert.True(t, Config{Tokens: ParseTokens("alice:t")}.Enabled())
}

// A token-only deployment on a routable address is allowed: it has a credential.
func TestTokenOnlyConfigMayBindRoutably(t *testing.T) {
	cfg := Config{Addr: "0.0.0.0:50051", Tokens: ParseTokens("alice:t")}
	assert.NoError(t, cfg.Validate(), "tokens are a credential, so the port is not open")
}

// A malformed token entry refuses the start even on loopback: the named
// principal would silently be unable to authenticate, and the symptom would
// appear much later as a confusing rejection.
func TestMalformedTokensRefuseToStartEvenOnLoopback(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:50051", Tokens: ParseTokens("alice")}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
}

// FromEnv reads both variables.
func TestFromEnvReadsBothCredentials(t *testing.T) {
	t.Setenv(EnvSecret, " s3cret ")
	t.Setenv(EnvTokens, " alice:token-a ")

	cfg := FromEnv("127.0.0.1:50051")
	assert.Equal(t, "s3cret", cfg.Secret)
	assert.Equal(t, 1, cfg.Tokens.Len())
}

// --- the no-early-exit property ---

// Resolving does the same work whichever entry matches.
//
// The loop deliberately does not return on the first match. A map lookup or an
// early return would make the time taken depend on which entry matched — and on
// whether any did — which is the leak subtle.ConstantTimeCompare exists to
// prevent on the shared-secret path. This test cannot measure time reliably, so
// it asserts the OBSERVABLE consequence: every principal in the set resolves,
// including ones that sort before and after the matching entry, and resolution
// is stable across repeated calls.
func TestEveryPrincipalResolvesRegardlessOfPosition(t *testing.T) {
	entries := []string{"aaa:ta", "bbb:tb", "mmm:tm", "yyy:ty", "zzz:tz"}
	set := ParseTokens(strings.Join(entries, ","))

	for _, entry := range entries {
		name, token, _ := strings.Cut(entry, ":")
		actor, ok := set.Resolve(token)
		require.True(t, ok, "%s must resolve", name)
		assert.Equal(t, name, actor.ID())

		// Repeated resolution gives the same answer: nothing is consumed or
		// mutated by a lookup.
		again, ok := set.Resolve(token)
		require.True(t, ok)
		assert.Equal(t, actor.ID(), again.ID())
	}
}

// A large set still resolves, so the no-early-exit loop is correct at size.
func TestLargeTokenSetResolvesAllPrincipals(t *testing.T) {
	var entries []string
	for i := 0; i < 64; i++ {
		entries = append(entries, principalName(i)+":token-"+principalName(i))
	}
	set := ParseTokens(strings.Join(entries, ","))
	require.Equal(t, 64, set.Len())

	for i := 0; i < 64; i++ {
		actor, ok := set.Resolve("token-" + principalName(i))
		require.True(t, ok, "principal %d must resolve", i)
		assert.Equal(t, principalName(i), actor.ID())
	}

	_, ok := set.Resolve("token-nobody")
	assert.False(t, ok)
}

func principalName(i int) string {
	return "p" + string(rune('a'+i/26)) + string(rune('a'+i%26))
}
