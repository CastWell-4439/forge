package identity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Actor survives a JSON round-trip with its principal intact.
//
// This was a real defect rather than a missing feature. The fields are unexported
// so that verification cannot be asserted by assignment, and encoding/json only
// writes exported fields — so every Actor encoded as `{}`. Two places persist
// one: hitl.Response and policy.Decision. A HITL approval therefore wrote its
// decision and an empty subject, and reading it back said "unattributed".
func TestActorSurvivesJSONRoundTrip(t *testing.T) {
	original := NewDeclared("alice")

	encoded, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Actor
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	id, declared := decoded.Declared()
	assert.True(t, declared, "the principal must survive")
	assert.Equal(t, "alice", id)
	assert.False(t, decoded.Verified(), "a declaration does not become verification by being stored")
}

// A verified actor keeps BOTH its flag and the name of what verified it.
//
// The verifier matters as much as the flag: "verified" with no named mechanism is
// an audit entry that says "trust me", and dropping it would leave the stored row
// unable to distinguish a real check from an assumed one.
func TestVerifiedActorSurvivesJSONRoundTrip(t *testing.T) {
	original := NewVerified("alice", "per-principal-token")

	encoded, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Actor
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, "alice", decoded.ID())
	assert.True(t, decoded.Verified())
	assert.Equal(t, "per-principal-token", decoded.Verifier())
}

// THE SECURITY PROPERTY: a payload cannot manufacture a verified actor.
//
// Unmarshalling goes through the constructors, so "verified with no verifier" is
// downgraded exactly as it is everywhere else. Without that, JSON would be a
// bypass around the rule the unexported field exists to enforce: anyone able to
// write a stored row could claim a checked identity.
func TestForgedVerificationIsRejectedOnDecode(t *testing.T) {
	payloads := []string{
		`{"id":"attacker","verified":true}`,
		`{"id":"attacker","verified":true,"verifier":""}`,
		`{"id":"attacker","verified":true,"verifier":"   "}`,
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			var decoded Actor
			require.NoError(t, json.Unmarshal([]byte(payload), &decoded))

			assert.False(t, decoded.Verified(),
				"verification without a named verifier must not survive decoding")
			assert.Equal(t, "attacker", decoded.ID(),
				"but the declared principal is still recorded honestly")
		})
	}
}

// The zero Actor encodes to an object with nothing in it, and decodes back to
// the zero Actor.
//
// "Nothing was declared" is a fact worth storing, and it must not become an
// error: rows written before this fix hold `{}`, and refusing them would make the
// affected records unreadable rather than merely unattributed.
func TestZeroActorRoundTrips(t *testing.T) {
	var original Actor

	encoded, err := json.Marshal(original)
	require.NoError(t, err)
	assert.JSONEq(t, `{"verified":false}`, string(encoded))

	var decoded Actor
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	_, declared := decoded.Declared()
	assert.False(t, declared)
}

// A stored row that predates this fix holds `{}` and must still decode.
//
// The identity in those rows is already lost — that is the damage the defect did.
// What this guarantees is that reading them does not fail, so the rest of the
// record (the decision, the feedback) stays usable and the gap appears as
// "unattributed" rather than as a broken audit trail.
func TestLegacyEmptyObjectDecodes(t *testing.T) {
	var decoded Actor
	require.NoError(t, json.Unmarshal([]byte(`{}`), &decoded))

	_, declared := decoded.Declared()
	assert.False(t, declared, "an old row reports no identity rather than refusing to load")
}

// Sanitisation applies on decode as well as on construction, so a stored value
// cannot smuggle control characters into audit output after passing through the
// database.
func TestDecodedValuesAreSanitised(t *testing.T) {
	var decoded Actor
	require.NoError(t, json.Unmarshal([]byte(`{"id":"alice\nadmin","verified":false}`), &decoded))

	assert.NotContains(t, decoded.ID(), "\n")
	assert.Contains(t, decoded.ID(), "alice")
}

// An outright malformed payload is an error: it is a decoding failure rather
// than a missing identity, and a caller should hear about it.
func TestMalformedPayloadIsAnError(t *testing.T) {
	var decoded Actor
	assert.Error(t, json.Unmarshal([]byte(`"not an object"`), &decoded))
	assert.Error(t, json.Unmarshal([]byte(`{"id":123}`), &decoded))
}
