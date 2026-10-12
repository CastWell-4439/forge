package grpcauth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedClock returns a clock pinned to an instant, so expiry is tested at an
// exact boundary rather than by sleeping and hoping.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// at builds a token list whose single entry expires at the given instant.
func at(instant time.Time) string {
	return "alice:token-a:expires=" + instant.Format(time.RFC3339)
}

// A token with an expiry in the future works, and the same parse reports it as
// live rather than merely accepted.
func TestTokenBeforeItsExpiryWorks(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	set := ParseTokens(at(expiry)).WithClock(fixedClock(expiry.Add(-time.Minute)))

	require.NoError(t, set.Error())
	actor, ok := set.Resolve("token-a")
	require.True(t, ok, "a token before its expiry must work")
	assert.Equal(t, "alice", actor.ID())
}

// A token whose expiry has passed stops working.
func TestTokenAfterItsExpiryStopsWorking(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	set := ParseTokens(at(expiry)).WithClock(fixedClock(expiry.Add(time.Minute)))

	_, ok := set.Resolve("token-a")
	assert.False(t, ok, "an expired token must not authenticate")
}

// The boundary itself: at the stated instant the token is already gone.
//
// "Expires at T" means the last valid moment is before T. Choosing the other
// reading would leave a one-second window nobody agreed to, and a test is the
// only place that distinction is ever visible.
func TestExpiryBoundaryIsExclusive(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	before := ParseTokens(at(expiry)).WithClock(fixedClock(expiry.Add(-time.Nanosecond)))
	_, ok := before.Resolve("token-a")
	assert.True(t, ok, "one nanosecond before the expiry the token is still valid")

	exactly := ParseTokens(at(expiry)).WithClock(fixedClock(expiry))
	_, ok = exactly.Resolve("token-a")
	assert.False(t, ok, "at the stated instant it has expired")
}

// A token with no expiry never expires, however far the clock moves.
func TestTokenWithoutExpiryNeverExpires(t *testing.T) {
	far := time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	set := ParseTokens("alice:token-a").WithClock(fixedClock(far))

	actor, ok := set.Resolve("token-a")
	require.True(t, ok)
	assert.Equal(t, "alice", actor.ID())
}

// An unparseable expiry is REFUSED rather than ignored.
//
// Ignoring it is the dangerous option: the operator believes the token dies on a
// date, and it never does — a credential outliving its stated life, which is
// exactly what an expiry exists to prevent.
func TestUnparseableExpiryIsRefused(t *testing.T) {
	for _, raw := range []string{
		"alice:token-a:expires=tomorrow",
		"alice:token-a:expires=2026-13-45T99:99:99Z",
		"alice:token-a:expires=",
		"alice:token-a:expires=2026-06-01", // date without a time is not RFC 3339
	} {
		set := ParseTokens(raw)
		require.Error(t, set.Error(), "input %q must be refused", raw)
		assert.Contains(t, set.Error().Error(), "expires", "and the message must name the field")
	}
}

// The digest form accepts an expiry too, in either order of the two colons.
func TestDigestFormAcceptsAnExpiry(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	entry := "alice:" + digestOf("token-a") + ":expires=" + expiry.Format(time.RFC3339)

	live := ParseTokens(entry).WithClock(fixedClock(expiry.Add(-time.Hour)))
	_, ok := live.Resolve("token-a")
	require.True(t, ok, "a hashed token with an expiry must work before it")

	dead := ParseTokens(entry).WithClock(fixedClock(expiry.Add(time.Hour)))
	_, ok = dead.Resolve("token-a")
	assert.False(t, ok, "and stop after it")
}

// An expired token and an unknown one are indistinguishable, so a reply cannot
// be used to learn that a credential was once real.
func TestExpiredAndUnknownTokensAreIndistinguishable(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	set := ParseTokens(at(expiry)).WithClock(fixedClock(expiry.Add(time.Hour)))

	_, expired := set.Resolve("token-a")
	_, unknown := set.Resolve("never-existed")

	assert.False(t, expired)
	assert.False(t, unknown)
	assert.Equal(t, expired, unknown, "the two answers must be identical, not merely both false")
}

// --- rotation ---

// ROTATION: one principal may hold two tokens at once, and BOTH work.
//
// This is the whole basis of rotation, and it already held before this change —
// the point of the test is to pin it. A rotation that required a restart for the
// new token would be no rotation at all, and one that invalidated the old token
// the moment the new one appeared would drop every in-flight client.
func TestOnePrincipalMayHoldTwoLiveTokens(t *testing.T) {
	set := ParseTokens("alice:old-token,alice:new-token")

	oldActor, oldOK := set.Resolve("old-token")
	newActor, newOK := set.Resolve("new-token")

	require.True(t, oldOK, "the token being retired must still work during the overlap")
	require.True(t, newOK, "and the new one must work")
	assert.Equal(t, "alice", oldActor.ID())
	assert.Equal(t, "alice", newActor.ID())
}

// During a rotation the overlap is what makes it safe, and an expiry on the OLD
// token is how it ends: the new one keeps working after the old one dies.
func TestRotationEndsWhenTheOldTokenExpires(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	entry := "alice:old-token:expires=" + expiry.Format(time.RFC3339) + ",alice:new-token"

	// During the overlap both work.
	during := ParseTokens(entry).WithClock(fixedClock(expiry.Add(-time.Hour)))
	_, oldOK := during.Resolve("old-token")
	_, newOK := during.Resolve("new-token")
	assert.True(t, oldOK, "both are live during the overlap")
	assert.True(t, newOK)

	// After it, only the new one does — which is what completes the rotation
	// without another config change.
	after := ParseTokens(entry).WithClock(fixedClock(expiry.Add(time.Hour)))
	_, oldOK = after.Resolve("old-token")
	_, newOK = after.Resolve("new-token")
	assert.False(t, oldOK, "the retired token is gone")
	assert.True(t, newOK, "and the replacement remains")
}

// One expired token for a principal must not hide their live one.
//
// The loop accumulates matches rather than breaking, and expiry clears only the
// match it applies to — so a principal with a live token is still authenticated
// even when an earlier entry of theirs has expired.
func TestAnExpiredTokenDoesNotHideALiveOne(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// The EXPIRED entry is deliberately first, so an early "clear and stop"
	// implementation would fail this.
	entry := "alice:dead-token:expires=" + expiry.Format(time.RFC3339) + ",alice:live-token"

	set := ParseTokens(entry).WithClock(fixedClock(expiry.Add(time.Hour)))

	_, deadOK := set.Resolve("dead-token")
	assert.False(t, deadOK)

	actor, liveOK := set.Resolve("live-token")
	require.True(t, liveOK, "a later live token must still authenticate its principal")
	assert.Equal(t, "alice", actor.ID())
}

// --- counting ---

// Len counts TOKENS; PrincipalCount counts PEOPLE. During a rotation they differ,
// and reporting the wrong one misstates the deployment.
func TestLenCountsTokensAndPrincipalCountCountsPeople(t *testing.T) {
	set := ParseTokens("alice:old,alice:new,bob:b")

	assert.Equal(t, 3, set.Len(), "three credentials are live")
	assert.Equal(t, 2, set.PrincipalCount(), "belonging to two people")
	assert.Equal(t, []string{"alice", "bob"}, set.Names(), "and the names are distinct")
}

// Names lists each principal once, which is what a log line should show.
//
// This was found by testing the rotation case: the list read [alice alice bob],
// so a deployment with two people reported three.
func TestNamesAreNotRepeatedDuringARotation(t *testing.T) {
	set := ParseTokens("alice:old,alice:new,alice:newer")

	assert.Equal(t, []string{"alice"}, set.Names())
	assert.Equal(t, 3, set.Len())
	assert.Equal(t, 1, set.PrincipalCount())
}

// TokensFor reports a principal's credentials and how many have expired, so
// "every token expired" is distinguishable from "never configured".
func TestTokensForReportsExpiryCounts(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	entry := "alice:dead:expires=" + expiry.Format(time.RFC3339) + ",alice:live"
	set := ParseTokens(entry).WithClock(fixedClock(expiry.Add(time.Hour)))

	total, expired := set.TokensFor("alice")
	assert.Equal(t, 2, total)
	assert.Equal(t, 1, expired)

	// A principal who was never configured reports nothing at all.
	total, expired = set.TokensFor("nobody")
	assert.Zero(t, total)
	assert.Zero(t, expired)

	// And the name is matched case-insensitively, like every other name here.
	total, _ = set.TokensFor("ALICE")
	assert.Equal(t, 2, total)
}

// A principal whose every token has expired is still a configured principal:
// the count answers "who was issued credentials", not "who can get in".
func TestPrincipalCountIncludesTheExpired(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	set := ParseTokens(at(expiry)).WithClock(fixedClock(expiry.Add(time.Hour)))

	assert.Equal(t, 1, set.PrincipalCount(), "the principal was issued a credential")
	_, ok := set.Resolve("token-a")
	assert.False(t, ok, "even though it no longer admits them")

	// The distinction is visible through TokensFor, which is why it exists.
	_, expired := set.TokensFor("alice")
	assert.Equal(t, 1, expired)
}

// --- parsing mechanics ---

// A token containing a colon is still parsed whole, because only the FIRST colon
// separates the principal.
func TestTokenContainingAColonIsParsedWhole(t *testing.T) {
	set := ParseTokens("alice:par:t1")

	_, ok := set.Resolve("par:t1")
	assert.True(t, ok, "the secret is everything after the first colon")
}
