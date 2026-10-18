package grpcauth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Guarding the no-early-exit property.
//
// Tokens.Resolve compares EVERY principal and does not return on the first match.
// That is a security property, not a style choice: an early exit makes the time
// taken depend on which entry matched — and on whether any did — which lets an
// attacker with a controllable input shorten their search considerably.
//
// The property was claimed in the design record and, until this file, unguarded.
// Mutating the loop to `break` on match left every test in the package green: the
// WINNER is the same either way, so no assertion about the answer could notice.
//
// The fix was to make the work observable. ResolveCounting reports how many
// entries were examined, and these tests assert that number. A `break` changes
// it; nothing else does.

// EVERY entry is examined, whether or not one matches.
//
// This is the direct assertion of the property. It would fail for any
// implementation that stopped early — on the first match, on the last match, or
// on no match at all.
func TestEveryEntryIsExamined(t *testing.T) {
	entries := []string{"alice:a1", "bob:b1", "carol:c1", "dave:d1", "erin:e1"}
	set := ParseTokens(joinComma(entries))
	require.Equal(t, 5, set.Len())

	cases := map[string]string{
		"match at the first entry": "a1",
		"match in the middle":      "c1",
		"match at the last entry":  "e1",
		"no match at all":          "not-a-token",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, examined := set.ResolveCounting(token)
			assert.Equal(t, 5, examined,
				"every configured entry must be compared, regardless of where the match is")
		})
	}
}

// An EXPIRED match does not end the scan either.
//
// This is the case an early exit gets wrong in a way that is not merely timing:
// `break` on match would stop before reaching a later live entry that belongs to
// the same principal, silently refusing a credential that was issued precisely to
// replace the dead one.
func TestExpiredMatchDoesNotEndTheScan(t *testing.T) {
	set := ParseTokens(
		"alice:dead-token:expires=2020-01-01T00:00:00Z," +
			"bob:bob-token," +
			"alice:live-token:expires=2099-01-01T00:00:00Z",
	)
	require.Equal(t, 3, set.Len())

	// The expired token is rejected.
	actor, ok, examined := set.ResolveCounting("dead-token")
	assert.False(t, ok, "an expired token must not authenticate")
	assert.Equal(t, 3, examined, "and rejecting it must not shorten the scan")
	_, _ = actor, examined

	// The live entry that comes AFTER it still resolves.
	actor, ok, examined = set.ResolveCounting("live-token")
	require.True(t, ok, "a live entry after an expired one must still resolve")
	assert.Equal(t, "alice", actor.ID())
	assert.Equal(t, 3, examined, "whether the match is late or early, all entries are compared")
}

// The scan length is a function of the SET, never of the input.
//
// If any non-empty input could shorten the scan, the scan would leak information
// about the input — which is the whole thing being prevented.
//
// Empty input is excluded deliberately: it is rejected BEFORE the loop, so it
// examines nothing. That is not a leak, it is a different code path — the caller
// presented no credential at all. TestEmptySetExaminesNothing pins that behaviour
// separately.
func TestScanLengthDoesNotDependOnTheInput(t *testing.T) {
	set := ParseTokens("aaa:ta,bbb:tb,ccc:tc,ddd:td")

	var lengths []int
	for _, token := range []string{"ta", "tb", "tc", "td", "zzz", "a-much-longer-guess"} {
		_, _, examined := set.ResolveCounting(token)
		lengths = append(lengths, examined)
	}

	for i, n := range lengths {
		assert.Equal(t, lengths[0], n,
			"input %d produced a different scan length; the scan must not depend on the input", i)
	}
	assert.Equal(t, 4, lengths[0], "and the constant length is the whole set")
}

// Resolution is independent of ENTRY ORDER: reordering the same list changes no
// answer and no scan length.
func TestResolutionIsIndependentOfEntryOrder(t *testing.T) {
	entries := []string{"alice:alice-1", "bob:bob-1", "carol:carol-1", "alice:alice-2"}

	forward := ParseTokens(joinComma(entries))
	reversed := ParseTokens(joinComma(reverseCopy(entries)))

	for _, token := range []string{"alice-1", "bob-1", "carol-1", "alice-2", "absent"} {
		fActor, fOK, fN := forward.ResolveCounting(token)
		rActor, rOK, rN := reversed.ResolveCounting(token)

		assert.Equal(t, fOK, rOK, "resolution of %q must not depend on entry order", token)
		assert.Equal(t, fActor.ID(), rActor.ID(), "nor the principal, for %q", token)
		assert.Equal(t, fN, rN, "nor the scan length, for %q", token)
	}
}

// The property holds at the size a deployment reaches.
func TestLargeSetExaminesEveryEntry(t *testing.T) {
	var entries []string
	for i := 0; i < 64; i++ {
		entries = append(entries, indexedName(i)+":tok-"+indexedName(i))
	}
	set := ParseTokens(joinComma(entries))
	require.Equal(t, 64, set.Len())

	for _, token := range []string{"tok-" + indexedName(0), "tok-" + indexedName(63), "absent"} {
		_, _, examined := set.ResolveCounting(token)
		assert.Equal(t, 64, examined, "all 64 entries must be compared for %q", token)
	}
}

// A near miss does not match, and the digest is not a credential.
func TestNearMissDoesNotMatch(t *testing.T) {
	set := ParseTokens("alice:exact-token")

	for _, guess := range []string{
		"exact-toke", "exact-token ", "exact-tokenX", "Exact-Token",
	} {
		_, ok, _ := set.ResolveCounting(guess)
		assert.False(t, ok, "near-miss %q must not authenticate", guess)
	}

	sum := sha256.Sum256([]byte("exact-token"))
	_, ok, _ := set.ResolveCounting(hex.EncodeToString(sum[:]))
	assert.False(t, ok, "reading the digest must not be enough to authenticate")
}

// An empty or unconfigured set examines nothing, and says so.
func TestEmptySetExaminesNothing(t *testing.T) {
	var empty TokenSet

	_, ok, examined := empty.ResolveCounting("anything")
	assert.False(t, ok)
	assert.Zero(t, examined)

	configured := ParseTokens("alice:t")
	_, _, examined = configured.ResolveCounting("")
	assert.Zero(t, examined, "an empty token is rejected before scanning, not during")
}

// --- helpers ---

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

func reverseCopy(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[len(parts)-1-i] = p
	}
	return out
}

func indexedName(i int) string {
	return "p" + string(rune('a'+i/26)) + string(rune('a'+i%26))
}
