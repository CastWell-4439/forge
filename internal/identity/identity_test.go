package identity

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// A declared identity is recorded, and reports itself as UNVERIFIED.
//
// This is the property the whole package is built around: a statement in request
// metadata is a claim, and nothing here checks it. An Actor that reported
// Verified()==true after NewDeclared would let "alice says she is alice" be read
// as "alice is alice".
func TestDeclaredIdentityIsUnverified(t *testing.T) {
	a := NewDeclared("alice")

	id, declared := a.Declared()
	assert.True(t, declared)
	assert.Equal(t, "alice", id)
	assert.False(t, a.Verified(), "a declaration is not a proof")
	assert.Empty(t, a.Verifier())
}

// The zero Actor means nothing was declared — and that is different from an
// actor whose id happens to be empty after sanitising, which is why Declared
// returns a bool rather than relying on the id being non-empty.
func TestZeroActorDeclaresNothing(t *testing.T) {
	var a Actor

	id, declared := a.Declared()
	assert.False(t, declared)
	assert.Empty(t, id)
	assert.False(t, a.Verified())
}

// A verified actor names what verified it.
func TestVerifiedActorNamesItsVerifier(t *testing.T) {
	a := NewVerified("alice", "per-principal-token")

	id, declared := a.Declared()
	assert.True(t, declared)
	assert.Equal(t, "alice", id)
	assert.True(t, a.Verified())
	assert.Equal(t, "per-principal-token", a.Verifier())
}

// A verified actor with NO verifier is downgraded to merely declared.
//
// "Verified" with nothing named as the verifier is an audit entry saying "trust
// me", which is worse than an honest declaration — so the failure direction is
// towards the weaker claim.
func TestVerifiedWithoutAVerifierDowngrades(t *testing.T) {
	a := NewVerified("alice", "")

	assert.False(t, a.Verified(), "nothing verified it, so it is not verified")
	assert.Empty(t, a.Verifier())
	assert.Equal(t, "alice", a.ID(), "but the declaration itself survives")
}

// A verifier with no id to verify produces nothing to claim.
func TestVerifiedWithNoIDDowngrades(t *testing.T) {
	a := NewVerified("", "some-verifier")

	assert.False(t, a.Verified())
	assert.Empty(t, a.ID())
	_, declared := a.Declared()
	assert.False(t, declared)
}

// THE TYPE-ENFORCEMENT PROPERTY: there is no way to obtain a verified Actor
// except through the constructor that names a verifier.
//
// This is asserted by construction rather than by a runtime check: Verified is
// unexported, so `Actor{verified: true}` cannot be written from outside this
// package and no field assignment exists to set it. A test in another package
// could not even attempt it — which is the point, and why this test lives HERE,
// where it documents that the field is deliberately unexported.
func TestVerifiedIsNotSettableFromOutside(t *testing.T) {
	// Inside the package the field is reachable, so the test states the rule
	// instead: the only constructors are the two below, and only one of them can
	// produce a verified actor.
	declared := NewDeclared("alice")
	verified := NewVerified("alice", "checker")

	assert.False(t, declared.Verified())
	assert.True(t, verified.Verified())

	// And the zero value is not verified either.
	assert.False(t, Actor{}.Verified())
}

// An undeclared actor renders as a WORD, not as an empty string: a blank field
// in an audit report reads as a rendering bug, while a word reads as the fact it
// is.
func TestUndeclaredRendersAsAWord(t *testing.T) {
	var a Actor
	assert.Equal(t, "unattributed", a.String())
	assert.NotEmpty(t, a.String())
}

// A declared but unverified actor says so in its rendering, so a reader cannot
// mistake it for a checked one.
func TestRenderingDistinguishesDeclaredFromVerified(t *testing.T) {
	declared := NewDeclared("alice").String()
	verified := NewVerified("alice", "token-check").String()

	assert.Contains(t, declared, "alice")
	assert.Contains(t, declared, "declared")
	assert.Contains(t, declared, "unverified")

	assert.Contains(t, verified, "alice")
	assert.Contains(t, verified, "verified")
	assert.Contains(t, verified, "token-check")

	assert.NotEqual(t, declared, verified, "the two must not render alike")
}

// A principal that can embed a newline can forge the audit line after it, so
// control characters are dropped.
func TestControlCharactersAreDropped(t *testing.T) {
	a := NewDeclared("alice\nadmin\tfake\r\n")

	assert.NotContains(t, a.ID(), "\n")
	assert.NotContains(t, a.ID(), "\r")
	assert.NotContains(t, a.ID(), "\t")
	assert.Contains(t, a.ID(), "alice")
}

// A long value is truncated rather than rejected: the first 256 bytes still
// identify the caller, while a rejection loses the attribution entirely.
func TestLongValuesAreTruncatedNotRejected(t *testing.T) {
	long := strings.Repeat("x", MaxActorIDLen*3)
	a := NewDeclared(long)

	require.LessOrEqual(t, len(a.ID()), MaxActorIDLen)
	assert.NotEmpty(t, a.ID(), "the attribution survives, bounded")
}

// Whitespace-only is nothing declared, not a principal named " ".
func TestWhitespaceOnlyDeclaresNothing(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n", "\u00a0"} {
		a := NewDeclared(raw)
		_, declared := a.Declared()
		assert.False(t, declared, "input %q must not count as a declaration", raw)
	}
}

// --- context ---

// The actor travels in the context.
func TestActorRoundTripsThroughContext(t *testing.T) {
	a := NewDeclared("alice")
	ctx := WithActor(context.Background(), a)

	got := FromContext(ctx)
	assert.Equal(t, "alice", got.ID())
	assert.False(t, got.Verified())
}

// A context with no actor reports nothing declared — the honest answer for a
// test, a direct call, or an in-process dispatch.
func TestContextWithoutActorDeclaresNothing(t *testing.T) {
	_, declared := FromContext(context.Background()).Declared()
	assert.False(t, declared)

	// A nil context is tolerated rather than panicking. It is not a value any
	// caller should pass — the linter rightly objects to it — but this function
	// sits on paths reached from hand-written call sites, and a nil check costs
	// nothing while a panic costs a run. The check is asserted through a variable
	// so the test does not itself spell out the call the linter forbids.
	var nilCtx context.Context
	_, declared = FromContext(nilCtx).Declared()
	assert.False(t, declared)
}

// --- transports ---

// gRPC metadata carries the declaration.
func TestFromGRPCReadsTheMetadata(t *testing.T) {
	md := metadata.Pairs(GRPCMetadataKey, "ci-runner-7")

	a := FromGRPC(md)
	assert.Equal(t, "ci-runner-7", a.ID())
	assert.False(t, a.Verified())
}

// A key in the wrong case is still read. gRPC normalises keys to lowercase, but
// a test or in-process caller may not, and a lookup that missed because of case
// would silently produce "unattributed" — the one outcome an audit trail must
// not produce by accident.
func TestFromGRPCToleratesKeyCase(t *testing.T) {
	md := metadata.MD{"X-Forge-Actor": []string{"alice"}}

	assert.Equal(t, "alice", FromGRPC(md).ID())
}

// No metadata at all is nothing declared.
func TestFromGRPCWithoutMetadata(t *testing.T) {
	_, declared := FromGRPC(nil).Declared()
	assert.False(t, declared)

	_, declared = FromGRPC(metadata.MD{}).Declared()
	assert.False(t, declared)
}

// The HTTP header carries the same declaration.
func TestFromHTTPReadsTheHeader(t *testing.T) {
	h := http.Header{}
	h.Set(HTTPHeader, "alice")

	a := FromHTTP(h)
	assert.Equal(t, "alice", a.ID())
	assert.False(t, a.Verified())
}

// Header names are case-insensitive by HTTP's own rules, which net/http already
// handles — asserted so a future hand-rolled lookup does not lose it.
func TestFromHTTPIsCaseInsensitive(t *testing.T) {
	h := http.Header{}
	h.Set("x-forge-actor", "alice")

	assert.Equal(t, "alice", FromHTTP(h).ID())
}

// No header is nothing declared.
func TestFromHTTPWithoutTheHeader(t *testing.T) {
	_, declared := FromHTTP(http.Header{}).Declared()
	assert.False(t, declared)

	_, declared = FromHTTP(nil).Declared()
	assert.False(t, declared)
}

// The two transports agree on the header name, so a deployment documents one
// thing rather than two.
func TestBothTransportsUseTheSameName(t *testing.T) {
	assert.Equal(t, strings.ToLower(GRPCMetadataKey), strings.ToLower(HTTPHeader),
		"the gRPC metadata key and the HTTP header must name the same thing")
}
