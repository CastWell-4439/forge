package state

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/castwell/forge/internal/forgex/model"
)

// ClaimValidationStatus is the outcome of one claim check.
type ClaimValidationStatus string

const (
	ClaimValidationPassed ClaimValidationStatus = "passed"
	ClaimValidationFailed ClaimValidationStatus = "failed"
)

// Claim scope values from the World State design. A claim's scope declares how far
// it applies, and it must be stated explicitly.
const (
	ScopeGlobal        = "global"
	ScopeScene         = "scene"
	ScopeShot          = "shot"
	ScopeAgentLocal    = "agent-local"
	ScopeTentativeOnly = "tentative-only"
)

// Envelope sources used by the claim pipeline so the failure taxonomy can match
// them without depending on this package.
const (
	EnvelopeSourceStateClaim     = "state_claim"
	EnvelopeSourceStateAuthority = "state_authority"
)

var validClaimScopes = map[string]bool{
	ScopeGlobal:        true,
	ScopeScene:         true,
	ScopeShot:          true,
	ScopeAgentLocal:    true,
	ScopeTentativeOnly: true,
}

// ClaimValidator checks whether a claim may leave Claim Space.
//
// It returns one validation record per check it ran. Any record whose status is
// "failed" rejects the claim; this mirrors how contract validation reports one row
// per validator.
type ClaimValidator interface {
	ValidateClaim(ctx context.Context, claim model.StateClaim) []model.StateValidation
}

// ClaimRule is one named claim check.
type ClaimRule struct {
	Name string
	// Check reports whether the claim passes, plus an explanatory message.
	Check func(claim model.StateClaim) (bool, string)
}

// RuleClaimValidator runs a fixed set of named rules.
type RuleClaimValidator struct {
	Rules []ClaimRule
}

// ValidateClaim runs every rule and records one validation per rule.
func (v RuleClaimValidator) ValidateClaim(ctx context.Context, claim model.StateClaim) []model.StateValidation {
	rules := v.Rules
	if len(rules) == 0 {
		rules = DefaultClaimRules()
	}
	out := make([]model.StateValidation, 0, len(rules))
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			break
		}
		ok, message := rule.Check(claim)
		status := ClaimValidationPassed
		if !ok {
			status = ClaimValidationFailed
		}
		out = append(out, newStateValidation(claim, rule.Name, status, message))
	}
	return out
}

// DefaultClaimValidator returns the built-in claim validator.
func DefaultClaimValidator() ClaimValidator {
	return RuleClaimValidator{Rules: DefaultClaimRules()}
}

// DefaultClaimRules is the built-in rule set.
//
// claim_evidence_present carries the design requirement that a claim must keep its
// evidence; claim_scope_valid carries the requirement that a state's scope be
// stated rather than left implicit.
func DefaultClaimRules() []ClaimRule {
	return []ClaimRule{
		{
			Name: "claim_key_present",
			Check: func(claim model.StateClaim) (bool, string) {
				if strings.TrimSpace(claim.Key) == "" {
					return false, "claim key is empty"
				}
				return true, "claim key is present"
			},
		},
		{
			Name: "claim_producer_present",
			Check: func(claim model.StateClaim) (bool, string) {
				if strings.TrimSpace(claim.Producer) == "" {
					return false, "claim producer is empty"
				}
				return true, "claim producer is present"
			},
		},
		{
			Name: "claim_evidence_present",
			Check: func(claim model.StateClaim) (bool, string) {
				for _, item := range claim.Evidence {
					if strings.TrimSpace(item) != "" {
						return true, "claim carries evidence"
					}
				}
				return false, "claim has no evidence"
			},
		},
		{
			Name: "claim_value_present",
			Check: func(claim model.StateClaim) (bool, string) {
				if len(claim.Value) == 0 {
					return false, "claim value is empty"
				}
				return true, "claim value is present"
			},
		},
		{
			Name: "claim_scope_valid",
			Check: func(claim model.StateClaim) (bool, string) {
				scope := strings.TrimSpace(claim.Scope)
				if scope == "" {
					return false, "claim scope is empty; it must be stated explicitly"
				}
				if !validClaimScopes[scope] {
					return false, fmt.Sprintf("claim scope %q is not one of global/scene/shot/agent-local/tentative-only", scope)
				}
				return true, "claim scope is valid"
			},
		},
	}
}

// SubmitInput is one request to move a claim from Claim Space to Fact Space.
type SubmitInput struct {
	World     model.WorldState
	Actor     string
	Claim     model.StateClaim
	Validator ClaimValidator
	Authority *AuthorityTable
	// Classify is applied to the rejection envelope when set. Wiring
	// failure.Classify in here keeps this package independent of the taxonomy.
	Classify func(model.ErrorEnvelope) model.ErrorEnvelope
}

// SubmitOutcome reports what happened to one claim.
type SubmitOutcome struct {
	World       model.WorldState
	Claim       model.StateClaim
	Validations []model.StateValidation
	// Envelope is set only when the claim was rejected, so a caller can persist a
	// classified failure. Never set on the success path.
	Envelope model.ErrorEnvelope
	Rejected bool
}

// WasRejected reports whether the claim was refused.
func (o SubmitOutcome) WasRejected() bool { return o.Rejected }

// SubmitClaim runs the Claim -> permission -> validation -> Fact pipeline.
//
// This is the layer the World State design calls for: AcceptClaim performs the
// state transition, and SubmitClaim decides whether the transition is allowed at
// all. AcceptClaim and RejectClaim keep their signatures.
func SubmitClaim(ctx context.Context, in SubmitInput) SubmitOutcome {
	claim := normalizeClaim(in.Claim)
	outcome := SubmitOutcome{World: in.World, Claim: claim}

	// 1. Permission. Skipped entirely when the authority table is absent or the
	// explicit switch inside it is off.
	if in.Authority.Enforcing() {
		if ok, reason := in.Authority.CanWrite(in.Actor, claim.Key); !ok {
			return reject(outcome, in, "state_authority", reason)
		}
	}

	// 2. Validation. Unconditional: a claim only becomes a fact once it passes.
	validator := in.Validator
	if validator == nil {
		validator = DefaultClaimValidator()
	}
	validations := validator.ValidateClaim(ctx, claim)
	outcome.Validations = validations
	for _, validation := range validations {
		if validation.Status == string(ClaimValidationFailed) {
			return reject(outcome, in, validation.Validator, validation.Message)
		}
	}

	// 3. Fact.
	claim.Status = model.StateAccepted
	outcome.Claim = claim
	outcome.World = AcceptClaim(in.World, claim)
	return outcome
}

// reject marks the claim rejected and builds the failure envelope for the caller.
func reject(outcome SubmitOutcome, in SubmitInput, validator, reason string) SubmitOutcome {
	claim := RejectClaim(outcome.Claim, reason)
	outcome.Claim = claim
	outcome.Rejected = true

	// Record the refusal as a validation row too, so the artifact trail explains
	// why the claim never became a fact.
	if validator != "" {
		outcome.Validations = append(outcome.Validations,
			newStateValidation(claim, validator, ClaimValidationFailed, reason))
	}

	source := EnvelopeSourceStateClaim
	if validator == "state_authority" {
		source = EnvelopeSourceStateAuthority
	}
	envelope := model.ErrorEnvelope{
		ID:        fmt.Sprintf("errenv-%s", claim.ID),
		RunID:     claim.RunID,
		Source:    source,
		Operation: "submit_claim",
		Message:   reason,
		RawError:  fmt.Sprintf("key=%s producer=%s validator=%s", claim.Key, claim.Producer, validator),
		Timestamp: time.Now().UTC(),
	}
	if in.Classify != nil {
		envelope = in.Classify(envelope)
	}
	outcome.Envelope = envelope
	return outcome
}

func newStateValidation(claim model.StateClaim, validator string, status ClaimValidationStatus, message string) model.StateValidation {
	return model.StateValidation{
		ID:        fmt.Sprintf("stateval-%s-%s", claim.ID, validator),
		RunID:     claim.RunID,
		ClaimID:   claim.ID,
		Key:       claim.Key,
		Status:    string(status),
		Validator: validator,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}

func normalizeClaim(claim model.StateClaim) model.StateClaim {
	claim.Key = strings.TrimSpace(claim.Key)
	claim.Producer = strings.TrimSpace(claim.Producer)
	claim.Scope = strings.TrimSpace(claim.Scope)
	return claim
}
