package identity

import (
	"encoding/json"
	"fmt"
)

// JSON encoding for Actor.
//
// This file exists because its absence was a real defect, not an omission.
//
// The type keeps its fields unexported on purpose (see identity.go): that is
// what stops a caller from asserting verification it did not perform. But
// encoding/json only serialises EXPORTED fields, so an Actor silently encoded as
// `{}` — and the two places that persist one, hitl.Response in the HITL store and
// policy.Decision, both round-trip through JSON.
//
// The consequence was severe and quiet: a HITL approval recorded who decided it,
// the row was written, and the column stored an empty object. Reading the record
// back said "unattributed". So the claim that D-68 made — that the audit trail
// knows who approved a release — was true in memory and false on disk. The
// existing tests did not catch it because they all read the in-memory Request;
// none of them went through the store.
//
// The fix keeps the field-level guarantee and adds one at the boundary:
// unmarshalling goes through the SAME constructors the rest of the code uses, so
// a payload cannot manufacture a verified actor it did not earn. Concretely,
// `{"id":"alice","verified":true}` with no verifier decodes to an unverified
// actor, because NewVerified downgrades exactly that case. The JSON boundary is
// therefore not a bypass around D-68 — it is another place the same rule applies.

// actorJSON is the wire form.
//
// The field names are lower-case and stable: they land in stored JSON in
// PostgreSQL, so renaming one later would silently stop old rows from decoding
// their identity.
type actorJSON struct {
	ID       string `json:"id,omitempty"`
	Verified bool   `json:"verified"`
	Verifier string `json:"verifier,omitempty"`
}

// MarshalJSON implements json.Marshaler.
//
// It encodes even an unattributed actor, as `{}` with all fields omitted: a
// zero-value Actor is a fact worth storing (it says "nothing was declared"),
// distinct from a NULL column (which says the field did not exist in that
// version). Emitting `{"verified":false}` for the zero value would also work,
// but omitting everything keeps stored rows small and readable.
func (a Actor) MarshalJSON() ([]byte, error) {
	return json.Marshal(actorJSON{
		ID:       a.id,
		Verified: a.verified,
		Verifier: a.verifier,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
//
// It deliberately does NOT restore the fields directly. The value is rebuilt
// through the constructors, which is what keeps a decoded actor subject to the
// same rule as a constructed one: verification requires a named verifier.
//
// A nil or empty payload yields the zero Actor rather than an error. Stored rows
// from before this fix hold `{}`, and refusing to read them would mean the audit
// trail could not be read at all — the data is already lost for those rows, and
// an error would replace "unattributed" with "unreadable".
func (a *Actor) UnmarshalJSON(data []byte) error {
	if a == nil {
		return fmt.Errorf("identity: cannot unmarshal into a nil Actor")
	}

	var wire actorJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("identity: decode actor: %w", err)
	}

	if wire.Verified {
		// The constructor requires a verifier, so a payload claiming
		// verification without naming one comes back unverified. That is the
		// intended outcome, not a gap: it means JSON cannot assert a check that
		// no mechanism performed.
		*a = NewVerified(wire.ID, wire.Verifier)
		return nil
	}
	*a = NewDeclared(wire.ID)
	return nil
}
