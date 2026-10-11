// Package identity records WHO caused something, as distinct from whether they
// were allowed to.
//
// Two questions that look alike and are not:
//
//	authentication   "do you hold the secret?"      — the gRPC shared secret
//	authorisation    "are you allowed to do this?"  — not built, and not this
//	identity         "which principal is this?"     — this package
//
// The first exists (D-62): the API is no longer reachable without a credential.
// But a SHARED secret cannot distinguish its holders, so the system could tell
// that a caller was permitted and not who they were. That gap shows up where it
// matters most: a HITL approval records a decision with no subject, so "who
// approved this release" has no answer — and the whole point of a human-in-the-
// loop gate is that a human is answerable for it.
//
// What this package is NOT: a permission system. It answers "who", never "may
// they". Authorisation needs a policy for what each principal can do, which is a
// separate decision with its own design.
//
// The design rule that shapes everything here: A DECLARATION IS NOT A PROOF.
//
// A caller states its identity in request metadata. Nothing verifies that
// statement yet — verifying it needs per-principal credentials, which is a key
// distribution problem this round does not take on. So the type keeps the two
// apart at the level of the compiler: Verified is unexported, and the only way
// to obtain a verified Actor is through a constructor that says who verified it.
// A downstream reader cannot accidentally treat "alice says she is alice" as
// "alice is alice", because there is no way to construct the second from the
// first.
//
// That matters beyond tidiness. This project has a rule it has applied three
// times — "not observed" is not "observed as absent" — and the same mistake in
// the other direction is treating an unchecked claim as a checked one. An
// exported bool would be set to true by the first caller who wanted it to be,
// and every later reader would have no way to tell which ones meant it.
package identity

import (
	"context"
	"strings"
	"unicode"
)

// MaxActorIDLen bounds a declared principal.
//
// The value arrives from a caller and lands in event payloads and audit output,
// so it is bounded rather than trusted. 256 is generous for a username, a CI job
// name or a service account and small enough that nobody can use the audit log
// as bulk storage.
const MaxActorIDLen = 256

// Actor is the principal that caused something.
//
// The zero value means "no identity was declared", which is a real and common
// state — every existing client declares nothing — and is deliberately distinct
// from an actor whose ID happens to be empty after sanitising.
type Actor struct {
	// id is the declared principal, sanitised. Empty means nothing was declared.
	id string
	// verified says whether something CHECKED the declaration. It is unexported
	// so that no caller can assert verification it did not perform.
	verified bool
	// verifier names what performed the check, for the audit trail. It is empty
	// exactly when verified is false, and the constructor is what keeps those two
	// in step.
	verifier string
}

// Declared returns the principal a caller stated, and whether anything was
// stated at all.
//
// The bool is not decoration: an empty id and no declaration are different
// facts, and a caller that collapses them loses the ability to say "nobody said
// who they were" — which is the honest report for every existing client.
func (a Actor) Declared() (string, bool) { return a.id, a.id != "" }

// ID returns the declared principal, or "" when nothing was declared.
func (a Actor) ID() string { return a.id }

// Verified reports whether the declaration was checked.
//
// It is a method rather than a field so the value cannot be set from outside,
// and it is the ONLY way a reader learns this — which is what stops an unchecked
// claim from being read as a checked one.
func (a Actor) Verified() bool { return a.verified }

// Verifier names what performed the check, or "" when nothing did.
func (a Actor) Verifier() string { return a.verifier }

// String renders the actor for a log line or an audit column.
//
// An undeclared actor renders as "unattributed" rather than as an empty string:
// a blank field in an audit report reads as a rendering bug, while a word reads
// as the fact it is. The same reasoning as reporting "not observed" instead of
// leaving a gap.
func (a Actor) String() string {
	if a.id == "" {
		return "unattributed"
	}
	if a.verified {
		return a.id + " (verified by " + a.verifier + ")"
	}
	return a.id + " (declared, unverified)"
}

// NewDeclared builds an actor from a caller's statement, without verifying it.
//
// This is what the transport layer calls. The result reports Verified()==false
// for the rest of its life unless something with authority to check it produces
// a new value — which is the whole point: verification is an act, not a field
// assignment.
func NewDeclared(raw string) Actor {
	return Actor{id: sanitize(raw)}
}

// NewVerified builds an actor whose declaration something checked.
//
// verifier must name the mechanism, and an empty name is refused: a verified
// actor with no recorded verifier is an audit entry that says "trust me", which
// is worse than an honest declaration. When verifier is empty the result is
// treated as merely declared, so the failure direction is towards the weaker
// claim rather than the stronger one.
func NewVerified(raw, verifier string) Actor {
	id := sanitize(raw)
	verifier = sanitize(verifier)
	if id == "" || verifier == "" {
		// Either there is nothing to verify or nothing verified it. Both mean the
		// stronger claim is not available, and saying so is the safe direction.
		return Actor{id: id}
	}
	return Actor{id: id, verified: true, verifier: verifier}
}

// sanitize bounds and cleans a declared value.
//
// Control characters are dropped rather than escaped: the value ends up in
// single-line audit output, and a principal that can embed a newline can forge
// the line after it. Truncation rather than rejection, because a long value is
// still a usable identifier for its first 256 bytes while a rejected one loses
// the attribution entirely.
func sanitize(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		// Drop anything that is not printable or is a line/paragraph separator:
		// those are exactly the characters that let a value reshape the output it
		// is written into.
		if r == '\n' || r == '\r' || r == '\t' || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= MaxActorIDLen {
			break
		}
	}

	// Truncation can leave trailing space that was interior before the cut.
	return strings.TrimSpace(b.String())
}

// actorKey carries the actor in a context.
type actorKey struct{}

// WithActor returns a context carrying the actor.
//
// A zero Actor is still stored: "this call was checked and no identity was
// declared" is a fact downstream code may want to distinguish from "nobody
// looked", and dropping the value would erase it.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// FromContext returns the actor recorded on a context.
//
// A context with no actor returns the zero Actor, whose Declared() is false.
// That is the honest answer for a call that arrived through a path that does not
// record identity — a test, a direct function call, an in-process dispatch —
// and it is why every consumer must check Declared() rather than testing the id
// against a placeholder.
func FromContext(ctx context.Context) Actor {
	if ctx == nil {
		return Actor{}
	}
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{}
}
