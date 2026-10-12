package grpcauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

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

// Principal is one named holder of a token.
type Principal struct {
	// Name is the principal the token authenticates.
	Name string
	// digest is the SHA-256 of the token. The token itself is not retained, so
	// even this process cannot print what it accepted.
	digest [sha256.Size]byte
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

		set.principals = append(set.principals, Principal{Name: name, digest: digest})
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

// Len is how many principals are configured.
func (t TokenSet) Len() int { return len(t.principals) }

// Error reports a malformed entry, if any.
func (t TokenSet) Error() error { return t.parseErr }

// Names lists the configured principals, sorted.
//
// It exposes names and never tokens: a log line or an error message that listed
// the credentials would defeat the point of storing digests.
func (t TokenSet) Names() []string {
	out := make([]string, 0, len(t.principals))
	for _, p := range t.principals {
		out = append(out, p.Name)
	}
	return out
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
// The cost is one SHA-256 and 32 byte comparisons per principal. With the handful
// of principals a deployment has, that is nothing next to a gRPC round trip.
func (t TokenSet) Resolve(token string) (identity.Actor, bool) {
	if !t.configured || token == "" || len(t.principals) == 0 {
		return identity.Actor{}, false
	}

	presented := sha256.Sum256([]byte(token))

	matched := ""
	for _, p := range t.principals {
		// Constant-time per comparison, and the result is accumulated rather than
		// branched on, so the loop body does the same work whichever entry
		// matches.
		if subtle.ConstantTimeCompare(presented[:], p.digest[:]) == 1 {
			matched = p.Name
		}
	}
	if matched == "" {
		return identity.Actor{}, false
	}
	return identity.NewVerified(matched, verifierName), true
}
