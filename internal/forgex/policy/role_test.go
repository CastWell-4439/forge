package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/castwell/forge/internal/identity"
)

// A role names a group of principals, and a rule can ask for it.
func TestRuleMatchesAnActorRole(t *testing.T) {
	cfg := &Config{
		Roles: map[string][]string{"deployers": {"alice", "bob"}},
		Rules: []Rule{{
			ID:     "deployers-may-deploy",
			When:   Condition{ToolName: "deploy", ActorRole: "deployers"},
			Action: ActionAllow,
			Reason: "deployers may deploy",
		}},
	}
	engine := NewEngine(cfg)
	c := contract("deploy", "high", "write")

	for _, member := range []string{"alice", "bob"} {
		got := engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared(member))
		assert.Equal(t, ActionAllow, got.Action, "%s is a deployer", member)
	}

	// A non-member does not match, so the rule does not apply.
	outsider := engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared("carol"))
	assert.NotEqual(t, "deployers may deploy", outsider.Reason,
		"a rule about the role must not apply to someone outside it")
}

// THE SUGAR PROPERTY: a rule naming a role means exactly the same as one rule
// per member.
//
// This is what makes the feature explainable rather than a second authorisation
// model. If the two ever disagreed, one of them would be a bug and there would
// be no way to tell which.
func TestARoleIsEquivalentToPerMemberRules(t *testing.T) {
	c := contract("deploy", "high", "write")

	viaRole := &Config{
		Roles: map[string][]string{"deployers": {"alice", "bob"}},
		Rules: []Rule{{
			ID: "role", When: Condition{ToolName: "deploy", ActorRole: "deployers"},
			Action: ActionAllow, Reason: "may deploy",
		}},
	}
	// The same authorisation written out longhand.
	expanded := &Config{Rules: []Rule{
		{ID: "alice", When: Condition{ToolName: "deploy", Actor: "alice"}, Action: ActionAllow, Reason: "may deploy"},
		{ID: "bob", When: Condition{ToolName: "deploy", Actor: "bob"}, Action: ActionAllow, Reason: "may deploy"},
	}}

	for _, name := range []string{"alice", "bob", "carol", ""} {
		actor := identity.NewDeclared(name)
		gotRole := NewEngine(viaRole).DecideFor("r", AuthorityL4, c, actor)
		gotFlat := NewEngine(expanded).DecideFor("r", AuthorityL4, c, actor)

		assert.Equal(t, gotFlat.Action, gotRole.Action,
			"the role form and the longhand form must agree for %q", name)
		assert.Equal(t, gotFlat.Reason, gotRole.Reason,
			"and the reason must agree too, or a reader sees two different stories for %q", name)
	}
}

// An UNDEFINED role matches nothing — the direction chosen so that a typo
// becomes a permission that does not apply, rather than a policy that will not
// load.
func TestUndefinedRoleMatchesNothing(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "typo",
		When:   Condition{ToolName: "deploy", ActorRole: "deployrs"}, // misspelled
		Action: ActionAllow,
		Reason: "intended to allow deployers",
	}}}
	engine := NewEngine(cfg)

	got := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))

	assert.NotEqual(t, ActionAllow, got.Action, "a rule naming a role that does not exist cannot fire")
}

// ...but it is NOT silent: the decision names the undefined role.
//
// Without this the softer direction would trade a visible startup failure for an
// invisible missing permission. The note is what makes the trade acceptable.
func TestUndefinedRoleIsNamedInTheReason(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:     "typo",
		When:   Condition{ToolName: "deploy", ActorRole: "deployrs"},
		Action: ActionAllow,
		Reason: "intended to allow deployers",
	}}}
	engine := NewEngine(cfg)

	got := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))

	assert.Contains(t, got.Reason, "deployrs",
		"the undefined role must be named, or the silent direction becomes a silent failure")
	assert.Contains(t, got.Reason, "undefined")
}

// Several undefined roles are all named, once each, in a stable order.
func TestEveryUndefinedRoleIsNamedOnce(t *testing.T) {
	cfg := &Config{Rules: []Rule{
		{ID: "a", When: Condition{ActorRole: "missing-b"}, Action: ActionDeny, Reason: "x"},
		{ID: "b", When: Condition{ActorRole: "missing-a"}, Action: ActionDeny, Reason: "y"},
		{ID: "c", When: Condition{ActorRole: "missing-a"}, Action: ActionDeny, Reason: "z"}, // repeated
	}}
	engine := NewEngine(cfg)

	got := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))

	assert.Contains(t, got.Reason, "missing-a")
	assert.Contains(t, got.Reason, "missing-b")

	// Named once each, not once per referencing rule.
	assert.Equal(t, 1, countOccurrences(got.Reason, "missing-a"))
	assert.Equal(t, 1, countOccurrences(got.Reason, "missing-b"))

	// Sorted, so two runs of the same policy produce the same string.
	i := indexOf(got.Reason, "missing-a")
	j := indexOf(got.Reason, "missing-b")
	assert.Less(t, i, j, "the names come back in a stable order")
}

// Role and member names are compared case-insensitively and trimmed, because a
// name an operator types is not case-significant — and a rule that silently
// stopped applying would be a permission that quietly vanished.
func TestRoleNamesAreNormalised(t *testing.T) {
	cfg := &Config{
		Roles: map[string][]string{"Deployers": {" Alice ", "BOB"}},
		Rules: []Rule{{
			ID: "role", When: Condition{ActorRole: "  deployers  "},
			Action: ActionAllow, Reason: "ok",
		}},
	}
	engine := NewEngine(cfg)

	for _, name := range []string{"alice", "ALICE", " alice ", "bob"} {
		got := engine.DecideFor("r", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared(name))
		assert.Equal(t, ActionAllow, got.Action, "member %q must match", name)
	}
}

// An empty role grants nothing, which is different from being undefined — but
// neither admits anyone, and only the undefined one is a policy mistake.
func TestEmptyRoleAdmitsNobody(t *testing.T) {
	// The rule's reason is a MARKER, and the assertion looks for the marker
	// rather than for "not allow".
	//
	// A non-matching rule falls through to the safe default, which allows a
	// low-risk read — so "not allow" cannot tell "the role did not match" from
	// "the default agreed". That is the same mistake this project already
	// recorded once, and it reappeared here because the assertion looks
	// convincing.
	const marker = "empty-role-granted"
	cfg := &Config{
		Roles: map[string][]string{"nobody": {}},
		Rules: []Rule{{
			ID: "empty", When: Condition{ActorRole: "nobody"}, Action: ActionAllow, Reason: marker,
		}},
	}
	engine := NewEngine(cfg)

	got := engine.DecideFor("r", AuthorityL4, contract("t", "low", "read"), identity.NewDeclared("alice"))
	assert.NotEqual(t, marker, got.Reason, "an empty role admits nobody")

	// And it is NOT reported as undefined: the role exists, it is just empty.
	assert.NotContains(t, got.Reason, "undefined",
		"a defined-but-empty role is not a policy mistake")
}

// An undeclared principal belongs to no role.
func TestUndeclaredActorBelongsToNoRole(t *testing.T) {
	const marker = "role-granted"
	cfg := &Config{
		Roles: map[string][]string{"deployers": {"alice"}},
		Rules: []Rule{{
			ID: "role", When: Condition{ActorRole: "deployers"}, Action: ActionAllow, Reason: marker,
		}},
	}
	engine := NewEngine(cfg)

	// No identity at all: the role rule cannot match, because nobody was named to
	// be a member. Asserted by marker for the reason above.
	got := engine.DecideFor("r", AuthorityL4, contract("t", "low", "read"), identity.Actor{})
	assert.NotEqual(t, marker, got.Reason, "an undeclared caller is in no role")
}

// A role and an explicit actor can be combined, and both must hold.
func TestRoleAndActorCanBeCombined(t *testing.T) {
	cfg := &Config{
		Roles: map[string][]string{"deployers": {"alice", "bob"}},
		Rules: []Rule{{
			ID: "alice-only-deployer",
			When: Condition{
				Actor:     "alice",
				ActorRole: "deployers",
			},
			Action: ActionAllow, Reason: "alice the deployer",
		}},
	}
	engine := NewEngine(cfg)
	c := contract("deploy", "high", "write")

	// Both hold.
	assert.Equal(t, ActionAllow, engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared("alice")).Action)

	// In the role but not the named actor.
	assert.NotEqual(t, ActionAllow, engine.DecideFor("r", AuthorityL4, c, identity.NewDeclared("bob")).Action,
		"being in the role is not enough when the rule also names an actor")
}

// A role can be combined with the other dimensions, including the risk ones.
func TestRoleCombinesWithOtherDimensions(t *testing.T) {
	cfg := &Config{
		Roles: map[string][]string{"deployers": {"alice"}},
		Rules: []Rule{{
			ID: "deployers-high-risk",
			When: Condition{
				ActorRole: "deployers",
				RiskLevel: "high",
			},
			Action: ActionRequireApproval, Reason: "deployers need approval for high risk",
		}},
	}
	engine := NewEngine(cfg)

	// Role holds, risk does not.
	low := engine.DecideFor("r", AuthorityL4, contract("deploy", "low", "write"), identity.NewDeclared("alice"))
	assert.NotEqual(t, ActionRequireApproval, low.Action)

	// Both hold.
	high := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))
	assert.Equal(t, ActionRequireApproval, high.Action)
}

// Roles do not disturb the existing dimensions: a policy with no roles behaves
// exactly as before.
func TestPolicyWithoutRolesIsUnchanged(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID: "high-deny", When: Condition{RiskLevel: "high"}, Action: ActionDeny, Reason: "high risk",
	}}}
	engine := NewEngine(cfg)

	got := engine.DecideFor("r", AuthorityL4, contract("deploy", "high", "write"), identity.NewDeclared("alice"))
	assert.Equal(t, ActionDeny, got.Action)
	assert.Equal(t, "high risk", got.Reason, "no undefined-role note is added when no role is referenced")
}

// The historical entry point keeps working with roles configured.
func TestDecideStillWorksWithRolesConfigured(t *testing.T) {
	const marker = "role-granted"
	cfg := &Config{
		Roles: map[string][]string{"deployers": {"alice"}},
		Rules: []Rule{{
			ID: "role", When: Condition{ActorRole: "deployers"}, Action: ActionAllow, Reason: marker,
		}},
	}

	got := NewEngine(cfg).Decide("r", AuthorityL4, contract("t", "low", "read"))

	assert.NotEqual(t, marker, got.Reason, "no declared identity means no role membership")
}

// --- helpers ---

func countOccurrences(haystack, needle string) int {
	n, from := 0, 0
	for {
		i := indexOf(haystack[from:], needle)
		if i < 0 {
			return n
		}
		n++
		from += i + len(needle)
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
