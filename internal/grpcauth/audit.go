package grpcauth

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
)

// Audit lines for authentication outcomes.
//
// The gap this fills is narrow and real: authentication was SILENT. A caller
// presenting a wrong token, a caller presenting an expired one, and a caller
// presenting nothing at all produced the same response and no record anywhere.
// The only authentication log lines in the process were the posture ones printed
// once at startup.
//
// That is enough for "is the port locked" and not enough for anything else. The
// questions an operator actually asks — who is calling, is someone guessing
// tokens, did a client break after a rotation — had no answer, because nothing
// was written down.
//
// Why structured log lines rather than a table: authentication happens before a
// workflow exists, and every event in the store is keyed by a WorkflowID (see
// storage.EventType). An audit row would therefore need a new table, new schema
// in both backends, and a new storage interface method — a large surface for a
// capability with no consumer yet. A structured line answers the questions above
// today and can be collected by whatever the deployment already runs; the field
// names below are the schema, so a table can be built from them later without
// redesigning anything.
//
// THE RULE THAT SHAPES EVERY FIELD: the audit log must never contain a usable
// credential.
//
// Audit logs are among the most-read, most-copied, most-exported artifacts a
// system produces: they go to files, log aggregators, tickets and screenshots.
// A line reading "alice presented token abc123" leaks the token to every one of
// those places, and a rotation triggered BECAUSE of a leak would then spread the
// leak further. So the token itself is never written. What is written is a short
// fingerprint, which answers "was this the same token as before" — the question
// an investigation actually asks — while being useless as a credential.

// auditPrefix marks these lines so a collector can route them without parsing
// the message. It is a constant rather than a per-call argument so no call site
// can invent a variant that the filter misses.
const auditPrefix = "AUDIT: auth"

// fingerprintLen is how many hex characters of the digest are kept.
//
// Eight is 32 bits: enough that two different tokens collide with probability
// around 1 in 4 billion, and short enough that the value cannot be mistaken for
// a credential or used as one. The comparison is against a hash, so even a full
// dump of the log does not yield a token.
const fingerprintLen = 8

// tokenFingerprint returns a short, non-reversible identifier for a presented
// credential.
//
// An empty credential returns "-" rather than the digest of the empty string:
// "the caller presented nothing" is a different fact from "the caller presented
// a token whose fingerprint happens to be this", and collapsing them would make
// the most common failure indistinguishable from the least common one.
func tokenFingerprint(token string) string {
	if strings.TrimSpace(token) == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:fingerprintLen]
}

// auditSuccess records a call that authenticated, and as whom.
//
// verified is reported rather than implied: "alice (verified)" and "alice
// (declared)" are different facts, and an audit reader deciding whether an
// approval was attributable needs to see which one applied. That distinction is
// the entire reason the Actor type separates them.
func auditSuccess(principal string, verified bool, mechanism string) {
	log.Printf("%s ok principal=%q verified=%t mechanism=%s",
		auditPrefix, principalOrUnattributed(principal), verified, mechanism)
}

// auditFailure records a rejected call.
//
// The REASON is deliberately coarse: "missing" versus "unknown" versus
// "expired" is collapsed into one word. The response already refuses to
// distinguish them (see UnauthenticatedError), and a log that distinguished them
// would hand an attacker the oracle the response withholds — the log is often
// readable by more people than the API is, not fewer.
//
// shared reports whether the deployment had a shared secret configured, which is
// a property of the DEPLOYMENT rather than of the attempt, and is safe to
// disclose: it tells an operator "this rejection is against a token-protected
// service" without telling them anything about the credential they guessed.
func auditFailure(fingerprint string, tokenConfigured, secretConfigured bool) {
	log.Printf("%s rejected reason=%s fingerprint=%s tokens_configured=%t secret_configured=%t",
		auditPrefix, "invalid_or_missing_credential", fingerprint, tokenConfigured, secretConfigured)
}

// principalOrUnattributed renders an empty principal as a word.
//
// The same reasoning as identity.Actor.String: a blank field in an audit line
// reads as a rendering bug, while a word reads as the fact it is.
func principalOrUnattributed(principal string) string {
	if strings.TrimSpace(principal) == "" {
		return "unattributed"
	}
	return principal
}
