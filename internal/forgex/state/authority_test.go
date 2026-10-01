package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchKeyPattern(t *testing.T) {
	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		{"character.*", "character.name", true},
		{"character.*", "character.a.b", true},
		{"character.*", "palette.tone", false},
		{"character.*", "character", false}, // needs the prefix including the dot
		{"*", "anything.at.all", true},
		{"final_render.status", "final_render.status", true},
		{"final_render.status", "final_render.other", false},
		{"", "character.name", false},
		{"character.*", "", false},
	}
	for _, tc := range cases {
		if got := MatchKeyPattern(tc.pattern, tc.key); got != tc.want {
			t.Errorf("MatchKeyPattern(%q, %q) = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
}

func TestLoadAuthorityRejectsMissingFile(t *testing.T) {
	if _, err := LoadAuthority(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("a missing authority file must be an error, never a silent fallback")
	}
}

func TestLoadAuthorityRejectsEnabledWithoutRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nenabled: true\nrules: []\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := LoadAuthority(path)
	if err == nil {
		t.Fatal("enabled with no rules must be rejected: it would deny every write")
	}
	if !strings.Contains(err.Error(), "no rules") {
		t.Fatalf("error should explain the empty rule list, got %v", err)
	}
}

func TestLoadAuthorityRejectsRuleWithoutOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.yaml")
	body := "version: 1\nenabled: true\nrules:\n  - pattern: \"a.*\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := LoadAuthority(path); err == nil {
		t.Fatal("a rule without an owner must be rejected")
	}
}

func TestLoadAuthorityReadsSwitchAndRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.yaml")
	body := "version: 1\nenabled: true\nrules:\n  - pattern: \"character.*\"\n    owner: character_agent\n    readers: [planner, renderer]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	table, err := LoadAuthority(path)
	if err != nil {
		t.Fatalf("LoadAuthority: %v", err)
	}
	if !table.Enforcing() {
		t.Fatal("enabled: true must enforce")
	}
	if len(table.Rules) != 1 || table.Rules[0].Owner != "character_agent" {
		t.Fatalf("unexpected rules: %+v", table.Rules)
	}
}

// The switch, not the presence of a table, decides whether checks run.
func TestAuthorityDisabledDoesNotEnforce(t *testing.T) {
	table := &AuthorityTable{Enabled: false, Rules: []AuthorityRule{{Pattern: "character.*", Owner: "owner"}}}
	if table.Enforcing() {
		t.Fatal("an explicit enabled:false must not enforce")
	}
	if ok, _ := table.CanWrite("anyone", "character.name"); !ok {
		t.Fatal("with enforcement off any actor may write")
	}
	if ok, _ := table.CanRead("anyone", "character.name"); !ok {
		t.Fatal("with enforcement off any actor may read")
	}
}

func TestAuthorityDeniesKeyWithNoRule(t *testing.T) {
	table := &AuthorityTable{Enabled: true, Rules: []AuthorityRule{{Pattern: "character.*", Owner: "character_agent"}}}
	ok, reason := table.CanWrite("character_agent", "palette.tone")
	if ok {
		t.Fatal("a key matching no rule must be denied")
	}
	if !strings.Contains(reason, "no state authority rule") {
		t.Fatalf("reason should say the key is uncovered, got %q", reason)
	}
}

func TestAuthorityOnlyOwnerMayWrite(t *testing.T) {
	table := &AuthorityTable{Enabled: true, Rules: []AuthorityRule{{
		Pattern: "character.*",
		Owner:   "character_agent",
		Readers: []string{"planner", "renderer"},
	}}}

	if ok, _ := table.CanWrite("character_agent", "character.name"); !ok {
		t.Fatal("the owner must be able to write")
	}
	ok, reason := table.CanWrite("planner", "character.name")
	if ok {
		t.Fatal("a reader must not be able to write")
	}
	if !strings.Contains(reason, "not the owner") {
		t.Fatalf("reason should say the actor is not the owner, got %q", reason)
	}
}

func TestAuthorityReadersMayRead(t *testing.T) {
	table := &AuthorityTable{Enabled: true, Rules: []AuthorityRule{{
		Pattern: "character.*",
		Owner:   "character_agent",
		Readers: []string{"planner"},
	}}}

	if ok, _ := table.CanRead("character_agent", "character.name"); !ok {
		t.Fatal("the owner must be able to read")
	}
	if ok, _ := table.CanRead("planner", "character.name"); !ok {
		t.Fatal("a listed reader must be able to read")
	}
	if ok, reason := table.CanRead("stranger", "character.name"); ok {
		t.Fatal("an unlisted actor must not be able to read")
	} else if !strings.Contains(reason, "may not read") {
		t.Fatalf("reason should explain the refusal, got %q", reason)
	}
}

// The first matching rule wins, so a specific rule placed first beats a broad one.
func TestAuthorityFirstMatchingRuleWins(t *testing.T) {
	table := &AuthorityTable{Enabled: true, Rules: []AuthorityRule{
		{Pattern: "character.name", Owner: "name_agent"},
		{Pattern: "character.*", Owner: "character_agent"},
	}}
	if ok, _ := table.CanWrite("name_agent", "character.name"); !ok {
		t.Fatal("the specific rule should win")
	}
	if ok, _ := table.CanWrite("character_agent", "character.name"); ok {
		t.Fatal("the broad rule must not apply to a key claimed by a specific rule")
	}
	if ok, _ := table.CanWrite("character_agent", "character.age"); !ok {
		t.Fatal("the broad rule should still cover other keys")
	}
}
