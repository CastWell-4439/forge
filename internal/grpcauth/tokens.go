package grpcauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/castwell/forge/internal/identity"
)

// Per-principal tokens: the credential that says WHO, not just WHETHER.
//
// A shared secret answers "may you call" and cannot answer "who are you",
// because every holder of it is indistinguishable from every other. That was
// stated plainly when the shared secret was added — "this is the port no longer
// being open, not the system being locked" — and this file is the second half.
//
// It also closes a promise the identity package made and could not keep. That
// package has two constructors, one for a declaration and one for something
// verified, and until now nothing called the second: `Verified()` was always
// false because nothing could verify anything. A token bound to a principal is
// the mechanism that produces one, which is why the token check is here rather
// than in a new package — it is authentication, and authentication already lives
// in this one.
//
// What it is NOT: a permission system. A token says who is calling. What they
// may do is decided by policy (Condition.Actor and Condition.ActorRole), and
// keeping the two apart is what lets a deployment grant a new capability without
// issuing a new token.

// EnvTokens is the environment variable holding the per-principal tokens.
//
//	FORGE_GRPC_TOKENS   a comma-separated list of principal:token or
//	                    principal:sha256:<hex> entries:
//
//	                      alice:sha256:9f86d081884c7d659a2feaa0c55ad015...
//	                      ci-runner:plain-token-for-local-testing
//
// The hashed form is preferred and is what the documentation recommends: the
// server then holds only a digest, so someone who can read the configuration
// cannot use what they read. The plaintext form exists because generating a
// digest before you can add the first principal makes the first configuration
// awkward, and an awkward security step is one that gets skipped.
const EnvTokens = "FORGE_GRPC_TOKENS"

// EnvClientToken is the token THIS process presents when calling out.
//
// It is separate from EnvTokens because the two ends need different things: a
// server holds the whole directory and needs to recognise anyone, while a client
// holds one credential and needs to be recognised. Reusing one variable would
// force every client to be trusted with every other client's token.
//
//	A worker presents FORGE_GRPC_CLIENT_TOKEN=its-own-token
//	the server holds  FORGE_GRPC_TOKENS=worker-a:...,worker-b:...
const EnvClientToken = "FORGE_GRPC_CLIENT_TOKEN"

// verifierName is what a token check is called in an audit trail.
//
// It is recorded on the Actor, so a reader can see not only that an identity was
// checked but what checked it. "Verified" with no named mechanism would be an
// audit entry that says "trust me".
const verifierName = "per-principal-token"

// plaintextDigestPrefix marks a token stored as a digest rather than in the
// clear. It is an explicit prefix rather than a length guess so that a mistyped
// entry fails loudly instead of being silently compared as a literal token.
const plaintextDigestPrefix = "sha256:"

// expiryKeyword introduces an optional expiry inside an entry.
//
//	alice:token
//	alice:sha256:<hex>
//	alice:token:expires=2026-01-01T00:00:00Z
//	alice:sha256:<hex>:expires=2026-01-01T00:00:00Z
//
// A keyword rather than a bare third field, because the field separator is ":"
// and BOTH things it would have to sit next to already contain colons: the
// digest prefix (sha256:) and an RFC 3339 timestamp (00:00:00). Splitting on
// colons and guessing which piece is the time would be a parser that works until
// someone writes a token containing a colon-like substring. "expires=" appears in
// no token format this project accepts, so its presence is unambiguous.
const expiryKeyword = "expires="

// Principal is one named holder of a token.
type Principal struct {
	// Name is the principal the token authenticates.
	Name string
	// digest is the SHA-256 of the token. The token itself is not retained, so
	// even this process cannot print what it accepted.
	digest [sha256.Size]byte
	// expiresAt is when the token stops being accepted. The zero value means it
	// never does.
	//
	// An absolute time rather than a time-to-live, and that is a deliberate
	// choice about what the configuration MEANS. A TTL is relative to whenever
	// the process happens to read it, so the same line describes a different
	// window every restart — and "when does this token die" becomes unanswerable
	// by reading the file. An absolute time makes the line self-describing.
	expiresAt time.Time
}

// expired reports whether the principal's token has stopped being valid.
func (p Principal) expired(now time.Time) bool {
	return !p.expiresAt.IsZero() && !now.Before(p.expiresAt)
}

// TokenSet is the resolved set of per-principal tokens.
//
// The zero value holds no tokens and verifies nobody, which is the correct
// meaning of "not configured".
type TokenSet struct {
	principals []Principal
	// configured records that a token list was present, so a typo can be
	// reported rather than looking like "nobody configured tokens".
	configured bool
	// parseErr is a malformed entry, kept rather than discarded so validation can
	// refuse to start on it.
	parseErr error
	// now is the clock, injected so expiry is testable without sleeping.
	//
	// A nil clock means time.Now, so a TokenSet built by ParseTokens works
	// without the caller knowing about this field.
	now func() time.Time
}

// WithClock returns a copy of the set that reads time from fn.
//
// It exists so an expiry test can place itself before or after a boundary
// exactly, instead of sleeping and hoping. The same pattern the policy engine
// and the stop arbiter already use.
func (t TokenSet) WithClock(fn func() time.Time) TokenSet {
	if fn == nil {
		return t
	}
	t.now = fn
	return t
}

// clock resolves the time source.
func (t TokenSet) clock() func() time.Time {
	if t.now == nil {
		return time.Now
	}
	return t.now
}

// TokensFromEnv reads the token set from the environment.
func TokensFromEnv() TokenSet {
	return ParseTokens(os.Getenv(EnvTokens))
}

// ParseTokens parses a comma-separated principal:token list.
//
// A malformed entry is recorded rather than skipped. Skipping would mean a
// deployment that mistyped a token starts up looking healthy while a principal
// silently cannot authenticate — the failure would appear as a confusing
// rejection much later. Recording it lets Validate refuse the start, where the
// mistake is still visible next to the line that caused it.
func ParseTokens(raw string) TokenSet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TokenSet{}
	}

	set := TokenSet{configured: true, principals: []Principal{}}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// The optional expiry is peeled off the END first, so the remaining
		// "principal:secret" is split exactly as it was before expiries existed.
		// Doing it the other way round would mean every later parse step had to
		// know that a third field might be present.
		entry, expiresAt, expiryRaw := splitExpiry(entry)

		name, secret, found := strings.Cut(entry, ":")
		name = strings.TrimSpace(name)
		secret = strings.TrimSpace(secret)
		if !found || name == "" || secret == "" {
			if set.parseErr == nil {
				// The entry is named but its token is not, and neither is echoed:
				// a malformed line is exactly where a secret is most likely to be
				// sitting in plain sight.
				set.parseErr = fmt.Errorf("malformed entry %d in %s: expected principal:token", len(set.principals)+1, EnvTokens)
			}
			continue
		}

		// An unparseable expiry is refused rather than ignored.
		//
		// Ignoring it would be the worst option: the operator believes the token
		// dies on a date, and it never does. That is a credential that outlives
		// its stated life, which is exactly what an expiry is there to prevent.
		if expiryRaw != "" && expiresAt.IsZero() {
			if set.parseErr == nil {
				set.parseErr = fmt.Errorf("entry for %q in %s: %s must be followed by an RFC 3339 timestamp (e.g. 2026-01-01T00:00:00Z)",
					name, EnvTokens, expiryKeyword)
			}
			continue
		}

		var digest [sha256.Size]byte
		switch {
		case strings.HasPrefix(secret, plaintextDigestPrefix):
			hexDigest := strings.TrimSpace(strings.TrimPrefix(secret, plaintextDigestPrefix))
			decoded, err := hex.DecodeString(hexDigest)
			if err != nil || len(decoded) != sha256.Size {
				if set.parseErr == nil {
					set.parseErr = fmt.Errorf("entry for %q in %s: %s must be followed by %d hex characters",
						name, EnvTokens, plaintextDigestPrefix, sha256.Size*2)
				}
				continue
			}
			copy(digest[:], decoded)
		default:
			digest = sha256.Sum256([]byte(secret))
		}

		set.principals = append(set.principals, Principal{Name: name, digest: digest, expiresAt: expiresAt})
	}

	// A list that is present but yields NO principals is a configuration mistake,
	// not a posture.
	//
	// Left alone it would be the worst of the three possibilities: Enabled() would
	// report a credential, so a routable deployment would be allowed to start —
	// and then nobody could authenticate, because there is no principal to match.
	// The operator would believe they had configured identities while the service
	// refused every caller. Refusing to start says so where the mistake is.
	//
	// This is checked after the loop and only when nothing else already failed, so
	// the more specific message about a single bad entry wins over this one.
	if len(set.principals) == 0 && set.parseErr == nil {
		set.parseErr = fmt.Errorf("%s is set but names no principal: expected principal:token entries", EnvTokens)
	}

	// A stable order, so two parses of the same input behave identically and a
	// test failure is reproducible.
	sort.Slice(set.principals, func(i, j int) bool {
		return set.principals[i].Name < set.principals[j].Name
	})
	return set
}

// Configured reports whether any token list was supplied.
func (t TokenSet) Configured() bool { return t.configured }

// Error reports a malformed entry, if any.
func (t TokenSet) Error() error { return t.parseErr }

// Names lists the DISTINCT principals configured, sorted.
//
// Distinct, because one principal may hold several tokens — that is how rotation
// works — and reporting "3 principals" for one principal with two tokens plus
// another would misstate the deployment. This was found by testing the rotation
// case: the count said three while the list read [alice alice bob].
//
// It exposes names and never tokens: a log line or an error message that listed
// the credentials would defeat the point of storing digests.
func (t TokenSet) Names() []string {
	seen := make(map[string]bool, len(t.principals))
	out := make([]string, 0, len(t.principals))
	for _, p := range t.principals {
		if seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// Len is how many TOKENS are configured, not how many principals.
//
// The two differ during a rotation, when one principal deliberately holds two.
// Both numbers are useful and neither is a substitute for the other: Len answers
// "how many credentials are live", Names answers "who". An audit wants the
// second and a leak-response wants the first.
func (t TokenSet) Len() int { return len(t.principals) }

// PrincipalCount is how many DISTINCT principals are configured.
func (t TokenSet) PrincipalCount() int { return len(t.Names()) }

// TokensFor lists the tokens configured for one principal, by index.
//
// It reports how many of that principal's tokens have expired as well, because a
// principal whose every token has expired is a different operational situation
// from one who was never configured — and the log line about "N principal(s)"
// would otherwise read the same in both cases.
func (t TokenSet) TokensFor(name string) (total, expired int) {
	now := t.clock()()
	key := normalizeName(name)
	for _, p := range t.principals {
		if normalizeName(p.Name) != key {
			continue
		}
		total++
		if p.expired(now) {
			expired++
		}
	}
	return total, expired
}

// normalizeName lowercases and trims a principal name for comparison.
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// splitExpiry peels an optional ":expires=<timestamp>" off the end of an entry.
//
// It returns the remaining entry, the parsed time, and the raw expiry text (empty
// when there was none). A present-but-unparseable expiry comes back as a zero
// time with non-empty raw text, which the caller turns into a refusal — the
// distinction between "no expiry stated" and "an expiry that makes no sense" has
// to survive this function.
func splitExpiry(entry string) (rest string, at time.Time, raw string) {
	idx := strings.LastIndex(entry, ":"+expiryKeyword)
	if idx < 0 {
		// Also accept the keyword at the very start, so a bare
		// "expires=..." is reported as malformed rather than silently treated as
		// a token.
		if strings.HasPrefix(entry, expiryKeyword) {
			return "", time.Time{}, strings.TrimSpace(strings.TrimPrefix(entry, expiryKeyword))
		}
		return entry, time.Time{}, ""
	}

	rest = entry[:idx]
	raw = strings.TrimSpace(entry[idx+1+len(expiryKeyword):])
	if raw == "" {
		// The keyword is present with nothing after it. Reported as written-but-
		// unparseable rather than as "no expiry": the operator stated an expiry
		// and gave no value, and silently reading that as "never expires" would
		// grant exactly the credential lifetime the line was trying to limit.
		return rest, time.Time{}, expiryKeyword
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return rest, time.Time{}, raw
	}
	return rest, parsed, raw
}

// Resolve reports which principal a presented token authenticates, if any.
//
// EVERY principal is compared, and the loop does not exit early.
//
// That is the part worth being careful about. A map lookup or a `return` on the
// first match would make the time taken depend on which entry matched, and on
// whether any did — and an attacker who can measure that can shorten their
// search dramatically. It is the same reasoning that put
// subtle.ConstantTimeCompare in the shared-secret path; doing the lookup through
// a hash map would reintroduce the leak in a new shape, because Go's map lookup
// is not constant time and its cost depends on the key.
//
// Expiry is checked INSIDE the loop, after the comparison, and the matched name
// is cleared rather than the loop broken out of. An expired token is therefore
// indistinguishable from an unknown one, which is deliberate: reporting "expired"
// separately would tell a caller that the token was once real, and that is
// information about the credential rather than about the request.
//
// The cost is one SHA-256, 32 byte comparisons and a time comparison per
// principal. With the handful of principals a deployment has, that is nothing
// next to a gRPC round trip.
func (t TokenSet) Resolve(token string) (identity.Actor, bool) {
	if !t.configured || token == "" || len(t.principals) == 0 {
		return identity.Actor{}, false
	}

	presented := sha256.Sum256([]byte(token))
	now := t.clock()()

	matched := ""
	for _, p := range t.principals {
		// Constant-time per comparison, and the result is accumulated rather than
		// branched on, so the loop body does the same work whichever entry
		// matches.
		if subtle.ConstantTimeCompare(presented[:], p.digest[:]) == 1 {
			if p.expired(now) {
				// Matched but no longer valid. Clearing rather than breaking keeps
				// the work per entry identical.
				matched = ""
				continue
			}
			matched = p.Name
		}
	}
	if matched == "" {
		return identity.Actor{}, false
	}
	return identity.NewVerified(matched, verifierName), true
}
