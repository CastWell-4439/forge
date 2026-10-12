package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/forgex/toolgw"
	"github.com/castwell/forge/internal/identity"
)

// boolPtr is a helper for the three-state optional fields.
func boolPtr(v bool) *bool { return &v }

// contract builds a tool contract with the fields the conditions match on.
func contract(name, risk, effect string) toolgw.ToolContract {
	return toolgw.ToolContract{
		Name:       name,
		RiskLevel:  toolgw.RiskLevel(risk),
		SideEffect: toolgw.SideEffect(effect),
	}
}

// A rule can name a principal, which is the dimension that did not exist.
func TestRuleMatchesOnActor(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "only-alice",
		When:   Condition{ToolName: "deploy", Actor: "alice"},
		Action: ActionAllow,
		Reason: "alice may deploy",
	}}}
	engine := NewEngine(cfg)

	alice := engine.DecideFor("run-1", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))
	assert.Equal(t, ActionAllow, alice.Action)
	assert.Equal(t, "alice may deploy", alice.Reason)

	// A different principal does not match, so the rule does not apply.
	bob := engine.DecideFor("run-1", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("bob"))
	assert.NotEqual(t, ActionAllow, bob.Action, "the rule is about alice, not about anyone")
}

// Actor matching ignores case: an identity arrives from an operator-written
// header, so "Alice" and "alice" are a typo rather than two principals.
func TestActorMatchingIsCaseInsensitive(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID: "case", When: Condition{Actor: "alice"}, Action: ActionDeny, Reason: "no",
	}}}
	engine := NewEngine(cfg)

	for _, spelling := range []string{"alice", "Alice", "ALICE", " alice "} {
		got := engine.DecideFor("run-1", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared(spelling))
		assert.Equal(t, ActionDeny, got.Action, "spelling %q must match", spelling)
	}
}

// A rule can require that an identity was DECLARED at all.
//
// This is how a deployment chooses the stricter posture without a code change:
// `actor_declared: false → deny` turns every unattributed call away.
func TestRuleCanRequireADeclaredActor(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "no-anonymous",
		When:   Condition{ActorDeclared: boolPtr(false)},
		Action: ActionDeny,
		Reason: "an identity is required",
	}}}
	engine := NewEngine(cfg)

	// Unattributed: the rule fires.
	anon := engine.DecideFor("run-1", AuthorityL4, contract("t", "low", "read"), identity.Actor{})
	assert.Equal(t, ActionDeny, anon.Action)

	// Attributed: it does not.
	named := engine.DecideFor("run-1", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared("alice"))
	assert.NotEqual(t, ActionDeny, named.Action)
}

// A rule requiring a VERIFIED actor matches nothing today, because nothing
// verifies anything.
//
// That is the honest behaviour rather than a limitation: the alternative —
// treating a declaration as verification — would make the rule apply to claims
// nobody checked. Writing such a rule now is safe: it takes effect the day
// verification exists and does nothing before then.
func TestVerifiedActorRuleMatchesNothingToday(t *testing.T) {
	// The action is Deny with a distinctive reason, not Allow: a non-matching
	// rule falls through to the safe default, which can itself produce "allow"
	// for a low-risk read. Asserting "not allow" then cannot tell "the rule did
	// not match" from "the default happened to agree", and the test would pass
	// for the wrong reason — which is how this test first failed.
	const marker = "verified callers only"
	cfg := &Config{Rules: []Rule{{
		ID:     "verified-only",
		When:   Condition{ActorVerified: boolPtr(true)},
		Action: ActionDeny,
		Reason: marker,
	}}}
	engine := NewEngine(cfg)

	// Even a named caller is unverified, so the rule does not fire — provable
	// because its marker reason is absent from the decision.
	named := engine.DecideFor("run-1", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared("alice"))
	assert.NotEqual(t, marker, named.Reason,
		"a declaration is not a verification, so a rule requiring one must not match")

	// The complementary rule does fire, which is what makes the pair usable.
	cfg2 := &Config{Rules: []Rule{{
		ID: "unverified", When: Condition{ActorVerified: boolPtr(false)},
		Action: ActionDeny, Reason: "unverified",
	}}}
	assert.Equal(t, ActionDeny,
		NewEngine(cfg2).DecideFor("run-1", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared("alice")).Action)
}

// Unset actor fields constrain nothing, the same three-state rule the other
// optional fields follow.
func TestUnsetActorFieldsDoNotConstrain(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "tool-only",
		When:   Condition{ToolName: "deploy"},
		Action: ActionAllow,
		Reason: "any caller",
	}}}
	engine := NewEngine(cfg)

	for _, actor := range []identity.Actor{
		{},
		identity.NewDeclared("alice"),
		identity.NewDeclared("bob"),
	} {
		got := engine.DecideFor("run-1", AuthorityL4, contract("deploy", "high", "write"), actor)
		assert.Equal(t, ActionAllow, got.Action, "a rule about the tool must not care who calls")
	}
}

// THE ORTHOGONALITY PROPERTY, asserted from both sides.
//
// Authority is what a RUN was granted; Actor is who is asking. If either
// collapsed into the other, one of these two tests would fail.
func TestActorAndAuthorityAreIndependent(t *testing.T) {
	// A rule that allows only alice, whatever the authority.
	byActor := &Config{Rules: []Rule{{
		ID: "actor-rule", When: Condition{Actor: "alice"}, Action: ActionAllow, Reason: "alice",
	}}}

	// The SAME actor at different authorities: the actor rule fires either way,
	// so the outcome does not depend on authority.
	for _, auth := range []AuthorityLevel{AuthorityL1, AuthorityL2, AuthorityL3, AuthorityL4} {
		got := NewEngine(byActor).DecideFor("r", auth, contract("t", "low", "read"), identity.NewDeclared("alice"))
		assert.Equal(t, ActionAllow, got.Action, "authority %s must not change an actor rule's match", auth)
	}

	// A rule that allows only at L4, whatever the actor.
	byAuthority := &Config{Rules: []Rule{{
		ID: "auth-rule", When: Condition{AuthorityAtLeast: "L4"}, Action: ActionAllow, Reason: "L4",
	}}}

	// The SAME authority with different actors: the authority rule fires either
	// way, so the outcome does not depend on the actor.
	for _, name := range []string{"", "alice", "bob"} {
		got := NewEngine(byAuthority).DecideFor("r", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared(name))
		assert.Equal(t, ActionAllow, got.Action, "actor %q must not change an authority rule's match", name)
	}
}

// Both dimensions can be required at once, and both must hold.
func TestActorAndAuthorityCanBeCombined(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "alice-at-L4",
		When:   Condition{Actor: "alice", AuthorityAtLeast: "L4"},
		Action: ActionAllow,
		Reason: "alice acting autonomously",
	}}}
	engine := NewEngine(cfg)
	c := contract("deploy", "high", "write")

	// Both hold.
	assert.Equal(t, ActionAllow,
		engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared("alice")).Action)

	// Actor holds, authority does not.
	assert.NotEqual(t, ActionAllow,
		engine.DecideFor("r", AuthorityL1, c, identity.NewDeclared("alice")).Action,
		"the right person without the authority is not enough")

	// Authority holds, actor does not.
	assert.NotEqual(t, ActionAllow,
		engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared("bob")).Action,
		"the authority without the right person is not enough")
}

// The decision records who it was about, so a denial names a subject.
func TestDecisionRecordsTheActor(t *testing.T) {
	engine := NewEngine(&Config{Rules: []Rule{{
		ID: "deny-bob", When: Condition{Actor: "bob"}, Action: ActionDeny, Reason: "bob may not",
	}}})

	got := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("bob"))

	assert.Equal(t, ActionDeny, got.Action)
	assert.Equal(t, "bob", got.Actor.ID())
	assert.False(t, got.Actor.Verified())
}

// An unattributed decision records that nobody was declared, distinct from one
// that names someone.
func TestDecisionWithoutAnActor(t *testing.T) {
	engine := NewEngine(nil)

	got := engine.Decide("r", AuthorityL2, contract("t", "low", "read"))

	_, declared := got.Actor.Declared()
	assert.False(t, declared)
	assert.Equal(t, "unattributed", got.Actor.String())
}

// The historical entry point keeps working and decides as unattributed.
//
// This is the no-regression property: every existing caller passes no identity,
// and must keep getting exactly what it got before.
func TestDecideStillWorksAndIsUnattributed(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID: "needs-actor", When: Condition{Actor: "alice"}, Action: ActionAllow, Reason: "alice only",
	}}}
	engine := NewEngine(cfg)

	// The old signature cannot express an actor, so the actor rule cannot fire.
	got := engine.Decide("r", AuthorityL4, contract("deploy", "high", "write"))

	assert.NotEqual(t, ActionAllow, got.Action, "a rule about a principal cannot match a caller with none")
	_, declared := got.Actor.Declared()
	assert.False(t, declared)
}

// The five original dimensions still behave exactly as before.
func TestOriginalDimensionsAreUnchanged(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "high-risk-deny",
		When:   Condition{RiskLevel: "high", SideEffect: "write"},
		Action: ActionDeny,
		Reason: "high risk write",
	}}}
	engine := NewEngine(cfg)

	got := engine.Decide("r", AuthorityL4, contract("deploy", "high", "write"))
	assert.Equal(t, ActionDeny, got.Action)
	assert.Equal(t, "high risk write", got.Reason)

	// And a low-risk read is untouched.
	safe := engine.Decide("r", AuthorityL4, contract("read", "low", "read"))
	assert.NotEqual(t, ActionDeny, safe.Action)
}
