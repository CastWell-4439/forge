package state

import (
	"context"
	"strings"
	"testing"

	"github.com/castwell/forge/internal/forgex/model"
)

func validClaim() model.StateClaim {
	claim := NewClaim("run-1", "claim-1", "character.name", "character_agent",
		map[string]any{"value": "Alice"}, []string{"artifact://character.png"})
	claim.Scope = ScopeGlobal
	return claim
}

func TestSubmitClaimAcceptsValidClaim(t *testing.T) {
	claim := validClaim()
	out := SubmitClaim(context.Background(), SubmitInput{
		World: model.WorldState{RunID: "run-1", Version: 1},
		Actor: claim.Producer,
		Claim: claim,
	})

	if out.Rejected {
		t.Fatalf("a valid claim must be accepted, got reason %q", out.Claim.Reason)
	}
	if out.Claim.Status != model.StateAccepted {
		t.Fatalf("claim status = %q, want accepted", out.Claim.Status)
	}
	if len(out.World.Entries) != 1 {
		t.Fatalf("expected one world state entry, got %d", len(out.World.Entries))
	}
	entry := out.World.Entries[0]
	if entry.Status != model.StateAccepted {
		t.Fatalf("entry status = %q, want accepted", entry.Status)
	}
	if entry.Scope != ScopeGlobal {
		t.Fatalf("entry scope = %q, want %q — scope must travel from the claim", entry.Scope, ScopeGlobal)
	}
	if len(out.Validations) == 0 {
		t.Fatal("expected the validation rows to be reported")
	}
	for _, v := range out.Validations {
		if v.Status != string(ClaimValidationPassed) {
			t.Errorf("validator %s should have passed: %s", v.Validator, v.Message)
		}
	}
	if out.Envelope.Message != "" {
		t.Fatalf("the success path must not produce a failure envelope, got %+v", out.Envelope)
	}
}

// A claim without evidence must never become a fact.
func TestSubmitClaimRejectsClaimWithoutEvidence(t *testing.T) {
	claim := validClaim()
	claim.Evidence = nil
	out := SubmitClaim(context.Background(), SubmitInput{World: model.WorldState{RunID: "run-1"}, Claim: claim})

	if !out.Rejected {
		t.Fatal("a claim without evidence must be rejected")
	}
	if out.Claim.Status != model.StateRejected {
		t.Fatalf("claim status = %q, want rejected", out.Claim.Status)
	}
	if !strings.Contains(out.Claim.Reason, "no evidence") {
		t.Fatalf("reason should mention the missing evidence, got %q", out.Claim.Reason)
	}
	if len(out.World.Entries) != 0 {
		t.Fatalf("a rejected claim must not add a world state entry, got %d", len(out.World.Entries))
	}
	if out.Envelope.Source != EnvelopeSourceStateClaim {
		t.Fatalf("envelope source = %q, want %q", out.Envelope.Source, EnvelopeSourceStateClaim)
	}
	if out.Envelope.Operation != "submit_claim" {
		t.Fatalf("envelope operation = %q, want submit_claim", out.Envelope.Operation)
	}
	// The refusal must also be visible as a failed validation row.
	var sawFailed bool
	for _, v := range out.Validations {
		if v.Status == string(ClaimValidationFailed) {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatal("the rejection should be recorded as a failed validation")
	}
}

func TestSubmitClaimRejectsMissingScope(t *testing.T) {
	claim := validClaim()
	claim.Scope = ""
	out := SubmitClaim(context.Background(), SubmitInput{World: model.WorldState{RunID: "run-1"}, Claim: claim})

	if !out.Rejected {
		t.Fatal("a claim without a scope must be rejected: the design requires the scope to be explicit")
	}
	if !strings.Contains(out.Claim.Reason, "scope") {
		t.Fatalf("reason should mention the scope, got %q", out.Claim.Reason)
	}
}

func TestSubmitClaimRejectsInvalidScope(t *testing.T) {
	claim := validClaim()
	claim.Scope = "galaxy"
	out := SubmitClaim(context.Background(), SubmitInput{World: model.WorldState{RunID: "run-1"}, Claim: claim})

	if !out.Rejected {
		t.Fatal("an unknown scope must be rejected")
	}
	if !strings.Contains(out.Claim.Reason, "galaxy") {
		t.Fatalf("reason should name the bad scope, got %q", out.Claim.Reason)
	}
}

func TestSubmitClaimRejectsEmptyKey(t *testing.T) {
	claim := validClaim()
	claim.Key = "   "
	out := SubmitClaim(context.Background(), SubmitInput{World: model.WorldState{RunID: "run-1"}, Claim: claim})
	if !out.Rejected {
		t.Fatal("a claim with a blank key must be rejected")
	}
}

// The permission layer runs before validation and reports its own envelope source.
func TestSubmitClaimRefusesActorThatIsNotOwner(t *testing.T) {
	authority := &AuthorityTable{Enabled: true, Rules: []AuthorityRule{{
		Pattern: "character.*",
		Owner:   "character_agent",
	}}}
	claim := validClaim()
	out := SubmitClaim(context.Background(), SubmitInput{
		World:     model.WorldState{RunID: "run-1"},
		Actor:     "intruder",
		Claim:     claim,
		Authority: authority,
	})

	if !out.Rejected {
		t.Fatal("a non-owner must not be able to write")
	}
	if out.Envelope.Source != EnvelopeSourceStateAuthority {
		t.Fatalf("envelope source = %q, want %q", out.Envelope.Source, EnvelopeSourceStateAuthority)
	}
	if len(out.World.Entries) != 0 {
		t.Fatal("a refused claim must not touch the world state")
	}
}

func TestSubmitClaimSkipsAuthorityWhenDisabled(t *testing.T) {
	authority := &AuthorityTable{Enabled: false, Rules: []AuthorityRule{{
		Pattern: "character.*",
		Owner:   "character_agent",
	}}}
	claim := validClaim()
	out := SubmitClaim(context.Background(), SubmitInput{
		World:     model.WorldState{RunID: "run-1"},
		Actor:     "someone_else",
		Claim:     claim,
		Authority: authority,
	})
	if out.Rejected {
		t.Fatalf("with the switch off, permission must not reject the claim: %s", out.Claim.Reason)
	}
}

// The caller wires failure.Classify in, so the state package stays independent of
// the taxonomy while the envelope still comes back classified.
func TestSubmitClaimAppliesClassifyHook(t *testing.T) {
	called := false
	out := SubmitClaim(context.Background(), SubmitInput{
		World: model.WorldState{RunID: "run-1"},
		Claim: func() model.StateClaim {
			claim := validClaim()
			claim.Evidence = nil
			return claim
		}(),
		Classify: func(env model.ErrorEnvelope) model.ErrorEnvelope {
			called = true
			env.Category = "state_claim_violation"
			env.Severity = "high"
			return env
		},
	})
	if !called {
		t.Fatal("the Classify hook must be applied to the rejection envelope")
	}
	if out.Envelope.Category != "state_claim_violation" {
		t.Fatalf("envelope category = %q, want state_claim_violation", out.Envelope.Category)
	}
}

// A custom validator replaces the default rule set.
func TestSubmitClaimHonoursCustomValidator(t *testing.T) {
	validator := RuleClaimValidator{Rules: []ClaimRule{{
		Name:  "always_ok",
		Check: func(model.StateClaim) (bool, string) { return true, "fine" },
	}}}
	claim := NewClaim("run-1", "claim-1", "anything", "", map[string]any{}, nil)
	out := SubmitClaim(context.Background(), SubmitInput{
		World:     model.WorldState{RunID: "run-1"},
		Claim:     claim,
		Validator: validator,
	})
	if out.Rejected {
		t.Fatalf("the custom validator should have accepted the claim: %s", out.Claim.Reason)
	}
	if len(out.Validations) != 1 || out.Validations[0].Validator != "always_ok" {
		t.Fatalf("unexpected validations: %+v", out.Validations)
	}
}

// Accepting a second claim for the same key stales the previous fact, so the
// pipeline keeps the no-silent-overwrite property.
func TestSubmitClaimStalesPreviousFactForSameKey(t *testing.T) {
	first := SubmitClaim(context.Background(), SubmitInput{World: model.WorldState{RunID: "run-1"}, Claim: validClaim()})
	second := validClaim()
	second.ID = "claim-2"
	second.Value = map[string]any{"value": "Bob"}

	out := SubmitClaim(context.Background(), SubmitInput{World: first.World, Claim: second})

	if out.Rejected {
		t.Fatalf("second claim should be accepted: %s", out.Claim.Reason)
	}
	var accepted, stale int
	for _, entry := range out.World.Entries {
		switch entry.Status {
		case model.StateAccepted:
			accepted++
		case model.StateStale:
			stale++
		}
	}
	if accepted != 1 || stale != 1 {
		t.Fatalf("expected one accepted and one stale entry, got accepted=%d stale=%d", accepted, stale)
	}
}
